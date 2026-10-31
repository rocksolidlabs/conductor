package ledger

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors a transition can end in.
var (
	// ErrCommandConflict says a command id was reused with other content.
	ErrCommandConflict = errors.New("ledger: command_conflict")
	// ErrTooManyCommands says the run has maxCommands receipts already.
	ErrTooManyCommands = errors.New("ledger: too_many_commands")
	// ErrTooLarge says a payload, a log or a crew is past its bound.
	ErrTooLarge = errors.New("ledger: too large")
	// ErrNoRun says the run has no row.
	ErrNoRun = errors.New("ledger: no such run")
	// ErrRunExists says CreateRun found a row of the run already: the id
	// is another run's, whose row stays as it was.
	ErrRunExists = errors.New("ledger: the run exists already")
)

// Tx is one transition: the rows it puts, the events it records and the
// command it answers are committed together, or not at all, and the run's
// revision is bumped once with them.
type Tx struct {
	ctx   context.Context
	tx    *sql.Tx
	runID string
	now   time.Time
}

// Now is the transaction's time, one for all it writes.
func (t *Tx) Now() time.Time { return t.now }

// Run reads the run's row within the transaction. The ledger's readers
// (Run, Runs, Tasks…) must not be called inside a transition: there is one
// connection, and it is the transaction's until it commits.
func (t *Tx) Run() (RunRow, bool, error) {
	row := t.tx.QueryRowContext(t.ctx, `SELECT `+runCols+` FROM runs WHERE id = ?`, t.runID)
	var r RunRow
	var paused int
	var created, closed string
	switch err := row.Scan(&r.ID, &r.CrewID, &r.Crew, &r.Base, &r.State, &r.Outcome, &paused, &r.Revision, &r.ContinuedFrom, &r.ContinuedBy, &created, &closed, &r.PausedWhy, &r.Manifest, &r.Ticket, &r.Package); {
	case errors.Is(err, sql.ErrNoRows):
		return RunRow{}, false, nil
	case err != nil:
		return RunRow{}, false, err
	}
	r.Paused, r.CreatedAt, r.ClosedAt = paused != 0, unstamp(created), unstamp(closed)
	return r, true, nil
}

// Transition runs fn in one transaction on the run with the given ID and,
// when fn returns nil, bumps the run's revision and commits; the new
// revision is returned. The run's row must exist by the end (fn may put
// it). fn's error, or the commit's, undoes everything. ErrReadOnly when the
// ledger takes no writes.
func (l *Ledger) Transition(ctx context.Context, runID string, fn func(*Tx) error) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.readOnly {
		return 0, fmt.Errorf("%w: %s", ErrReadOnly, l.why)
	}
	if l.closed {
		return 0, ErrClosed
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, cancelledAs(ctx, err)
	}
	defer tx.Rollback()
	t := &Tx{ctx: ctx, tx: tx, runID: runID, now: time.Now().UTC()}
	// Every error of the transaction, fn's and the statements' as the
	// commit's, is told as the context's once the context has ended:
	// database/sql's rollback of a cancelled transaction races each step,
	// which then says sql.ErrTxDone (Codex's review of #367).
	if err := fn(t); err != nil {
		return 0, cancelledAs(ctx, err)
	}
	res, err := tx.ExecContext(ctx, "UPDATE runs SET revision = revision + 1 WHERE id = ?", runID)
	if err != nil {
		return 0, cancelledAs(ctx, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, ErrNoRun
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM runs WHERE id = ?", runID).Scan(&rev); err != nil {
		return 0, cancelledAs(ctx, err)
	}
	if l.beforeCommit != nil {
		if err := l.beforeCommit(); err != nil {
			return 0, cancelledAs(ctx, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, cancelledAs(ctx, err)
	}
	return rev, nil
}

// cancelledAs is err as the transaction's caller should see it: the
// context's own error when the context ended under the transaction,
// whatever database/sql said of it (its rollback of a cancelled
// transaction races the commit, which then says sql.ErrTxDone), so that a
// stop is told from a store that failed (Codex's review of #367); err
// itself otherwise.
func cancelledAs(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil && (errors.Is(err, sql.ErrTxDone) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("ledger: %w", cerr)
	}
	return err
}

// Event records an event of the run, next in its sequence, with payload as
// JSON (at most MaxPayload bytes).
func (t *Tx) Event(kind string, payload any) error {
	body, err := encode(payload, MaxPayload)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO events (run_id, seq, at, kind, payload)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE run_id = ?), ?, ?, ?)`,
		t.runID, t.runID, stamp(t.now), kind, body)
	return err
}

// Command answers a command: the receipt it has already, when actor sent
// the same id with the same content before (found); ErrCommandConflict
// when with other content; else a new receipt, accepted, which Settle
// finishes. ErrTooManyCommands past the run's bound.
func (t *Tx) Command(actor, id, intent, fingerprint string) (prior CommandRow, found bool, err error) {
	row := t.tx.QueryRowContext(t.ctx, `SELECT intent, fingerprint, status, receipt, created_at FROM commands
		WHERE run_id = ? AND actor = ? AND id = ?`, t.runID, actor, id)
	var at string
	prior = CommandRow{RunID: t.runID, Actor: actor, ID: id}
	switch err := row.Scan(&prior.Intent, &prior.Fingerprint, &prior.Status, &prior.Receipt, &at); {
	case err == nil:
		prior.CreatedAt = unstamp(at)
		if prior.Fingerprint != fingerprint || prior.Intent != intent {
			return CommandRow{}, false, ErrCommandConflict
		}
		return prior, true, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return CommandRow{}, false, err
	}
	var n int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM commands WHERE run_id = ?", t.runID).Scan(&n); err != nil {
		return CommandRow{}, false, err
	}
	if n >= maxCommands {
		return CommandRow{}, false, ErrTooManyCommands
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO commands (run_id, actor, id, intent, fingerprint, status, receipt, created_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?)`, t.runID, actor, id, intent, fingerprint, CommandAccepted, stamp(t.now))
	return CommandRow{}, false, err
}

// Settle gives a command its outcome and receipt (JSON, at most MaxPayload
// bytes): done, failed or uncertain.
func (t *Tx) Settle(actor, id, status string, receipt any) error {
	body, err := encode(receipt, MaxPayload)
	if err != nil {
		return err
	}
	res, err := t.tx.ExecContext(t.ctx, "UPDATE commands SET status = ?, receipt = ? WHERE run_id = ? AND actor = ? AND id = ?",
		status, body, t.runID, actor, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("ledger: no command %s of %s", id, actor)
	}
	return nil
}

// PutRun puts the run's row, new or changed. The revision is the ledger's:
// what r says is ignored. continued_by is never cleared: a write of a row
// made before the run was continued (a stop's) keeps the link, so the run
// cannot be continued twice. The ticket and the package are a new row's
// alone: a row that exists keeps its own, since they are the run's input,
// written once by its first row. A run's first row is CreateRun's, which
// refuses an id the ledger has; PutRun, which takes a row that exists, is
// every later transition's.
func (t *Tx) PutRun(r RunRow) error {
	if err := t.checkRun(r); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO runs (`+runInsertCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET crew_id = excluded.crew_id, crew = excluded.crew, base = excluded.base, state = excluded.state,
			outcome = excluded.outcome, paused = excluded.paused, continued_from = excluded.continued_from,
			continued_by = CASE WHEN excluded.continued_by = '' THEN runs.continued_by ELSE excluded.continued_by END,
			closed_at = excluded.closed_at, paused_why = excluded.paused_why, manifest = excluded.manifest`,
		runInsertArgs(r)...)
	return err
}

// CreateRun puts a new run's first row: an insert that refuses an id the
// ledger has (ErrRunExists, that row untouched), the check and the write
// at once under the ledger's write lock, whatever another process, server
// or restart wrote. Every new run's first transaction writes its row so
// (a launch's, a planned launch's and a continuation's), never with
// PutRun, which would take another run's row as its own.
func (t *Tx) CreateRun(r RunRow) error {
	if err := t.checkRun(r); err != nil {
		return err
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO runs (`+runInsertCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`, runInsertArgs(r)...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("%w: %s", ErrRunExists, r.ID)
	}
	return nil
}

// runInsertCols are the columns a run's row is put with, in
// runInsertArgs' order (the revision the ledger's, 0 on an insert).
const runInsertCols = `id, crew_id, crew, base, state, outcome, paused, revision, continued_from, continued_by, created_at, closed_at, paused_why, manifest, ticket, package`

func runInsertArgs(r RunRow) []any {
	return []any{r.ID, r.CrewID, r.Crew, r.Base, r.State, r.Outcome, b2i(r.Paused), r.ContinuedFrom, r.ContinuedBy, stamp(r.CreatedAt), stamp(r.ClosedAt), r.PausedWhy, r.Manifest, r.Ticket, r.Package}
}

// checkRun refuses a run's row past its bounds, or one of another run.
func (t *Tx) checkRun(r RunRow) error {
	if len(r.Crew) > MaxCrew {
		return fmt.Errorf("%w: the crew is %d bytes", ErrTooLarge, len(r.Crew))
	}
	if len(r.PausedWhy) > MaxPayload {
		return fmt.Errorf("%w: the pause's words are %d bytes", ErrTooLarge, len(r.PausedWhy))
	}
	if len(r.Manifest) > MaxPayload {
		return fmt.Errorf("%w: the run's manifest is %d bytes", ErrTooLarge, len(r.Manifest))
	}
	if len(r.Package) > MaxPackage {
		return fmt.Errorf("%w: the run's package is %d bytes", ErrTooLarge, len(r.Package))
	}
	if len(r.Ticket) > maxTicketID {
		return fmt.Errorf("%w: the run's ticket id is %d bytes", ErrTooLarge, len(r.Ticket))
	}
	if r.ID != t.runID {
		return fmt.Errorf("ledger: run %s put in %s's transition", r.ID, t.runID)
	}
	return nil
}

// ErrContinued says the run named has been continued already, or has no
// row: a continuation is made once.
var ErrContinued = errors.New("ledger: the run was continued already")

// Continued marks the run from as continued by this transition's run: its
// continued_by, set once; ErrContinued when it is set already or the run
// has no row. The one write a transition makes to another run's row.
func (t *Tx) Continued(from string) error {
	res, err := t.tx.ExecContext(t.ctx, "UPDATE runs SET continued_by = ? WHERE id = ? AND continued_by = ''", t.runID, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: %s", ErrContinued, from)
	}
	return nil
}

// PutTask puts a task's row, new or changed.
func (t *Tx) PutTask(r TaskRow) error {
	if len(r.Repair) > MaxPayload {
		return fmt.Errorf("ledger: the repair of task %s is more than %d bytes", r.ID, MaxPayload)
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO tasks (run_id, id, member, kind, state, reason, attempts, result_attempt, result_commit, verdict, imported_from, repair, extra, withdrawn)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, id) DO UPDATE SET member = excluded.member, kind = excluded.kind, state = excluded.state, reason = excluded.reason,
			attempts = excluded.attempts, result_attempt = excluded.result_attempt, result_commit = excluded.result_commit, verdict = excluded.verdict,
			imported_from = excluded.imported_from, repair = excluded.repair, extra = excluded.extra, withdrawn = excluded.withdrawn`,
		t.runID, r.ID, r.Member, r.Kind, r.State, r.Reason, r.Attempts, r.ResultAttempt, r.ResultCommit, r.Verdict, r.ImportedFrom, r.Repair, r.Extra, r.Withdrawn)
	return err
}

// PutAttempt puts an attempt's row, new or changed.
func (t *Tx) PutAttempt(r AttemptRow) error {
	if len(r.Result) > MaxResult {
		return fmt.Errorf("ledger: the result of attempt %s/%d is more than %d bytes", r.TaskID, r.N, MaxResult)
	}
	if len(r.Checkpoint) > MaxCheckpoint {
		return fmt.Errorf("ledger: the checkpoint of attempt %s/%d is more than %d bytes", r.TaskID, r.N, MaxCheckpoint)
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO attempts (run_id, task_id, n, member, session_id, worktree, branch, start_commit, result_commit, state, stage, error, reserved_at, launched_at, prompted_at, closed_at, result, checkpoint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, task_id, n) DO UPDATE SET member = excluded.member, session_id = excluded.session_id, worktree = excluded.worktree,
			branch = excluded.branch, start_commit = excluded.start_commit, result_commit = excluded.result_commit, state = excluded.state,
			stage = excluded.stage, error = excluded.error, reserved_at = excluded.reserved_at, launched_at = excluded.launched_at,
			prompted_at = excluded.prompted_at, closed_at = excluded.closed_at, result = excluded.result, checkpoint = excluded.checkpoint`,
		t.runID, r.TaskID, r.N, r.Member, r.SessionID, r.Worktree, r.Branch, r.StartCommit, r.ResultCommit, r.State, r.Stage, r.Error,
		stamp(r.ReservedAt), stamp(r.LaunchedAt), stamp(r.PromptedAt), stamp(r.ClosedAt), r.Result, r.Checkpoint)
	return err
}

// PutMessage puts a message's row, new or changed (delivered, acked); a
// run keeps at most maxMessages, and a text at most MaxMessageText bytes.
func (t *Tx) PutMessage(r MessageRow) error {
	if len(r.Text) > MaxMessageText {
		return fmt.Errorf("ledger: message %d is more than %d bytes", r.Seq, MaxMessageText)
	}
	var n int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM messages WHERE run_id = ? AND seq != ?", t.runID, r.Seq).Scan(&n); err != nil {
		return err
	}
	if n >= maxMessages {
		return fmt.Errorf("%w: %d messages", ErrTooManyMessages, n)
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO messages (run_id, seq, from_member, to_member, text, created_at, typing_at, delivered_at, acked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, seq) DO UPDATE SET typing_at = excluded.typing_at, delivered_at = excluded.delivered_at, acked_at = excluded.acked_at`,
		t.runID, r.Seq, r.From, r.To, r.Text, stamp(r.CreatedAt), stamp(r.TypingAt), stamp(r.DeliveredAt), stamp(r.AckedAt))
	return err
}

// ErrTooManyMessages says the run keeps as many messages as it may.
var ErrTooManyMessages = errors.New("ledger: the run keeps as many messages as it may")

// Messages reads a run's messages in sequence.
func (l *Ledger) Messages(ctx context.Context, runID string) ([]MessageRow, error) {
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT seq, from_member, to_member, text, created_at, typing_at, delivered_at, acked_at FROM messages WHERE run_id = ? ORDER BY seq", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageRow
	for rows.Next() {
		r := MessageRow{RunID: runID}
		var created, typing, delivered, acked string
		if err := rows.Scan(&r.Seq, &r.From, &r.To, &r.Text, &created, &typing, &delivered, &acked); err != nil {
			return nil, err
		}
		r.CreatedAt, r.TypingAt, r.DeliveredAt, r.AckedAt = unstamp(created), unstamp(typing), unstamp(delivered), unstamp(acked)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutCheck puts a check's row, new or changed: a task's check or a step
// of a project manifest (Phase, PhaseCheck when empty; Tries at least 1,
// but for a readiness probe's, its own count, 0 when it ran no try);
// its log at most MaxLog, and the run's logs, the prerequisites' with the
// checks', at most MaxRunLogs in all: a log that would pass that is not
// kept (LogDropped), its hash and tail are. The row's own log, when it is
// replaced, counts no more.
func (t *Tx) PutCheck(r CheckRow) error {
	if len(r.Log) > MaxLog {
		return fmt.Errorf("%w: the log is %d bytes", ErrTooLarge, len(r.Log))
	}
	if err := rowText(r.URL, r.Manifest, r.Error); err != nil {
		return err
	}
	argv, err := encode(r.Argv, MaxPayload)
	if err != nil {
		return err
	}
	ports, err := encodePorts(r.Ports)
	if err != nil {
		return err
	}
	phase := r.Phase
	if phase == "" {
		phase = PhaseCheck
	}
	tries := max(r.Tries, 1)
	if phase == PhaseReady {
		tries = max(r.Tries, 0)
	}
	if r.LogDropped {
		r.Log = nil // dropped is dropped: no bytes behind the mark
	}
	if len(r.Log) > 0 {
		var kept int64
		if err := t.tx.QueryRowContext(t.ctx, `SELECT (SELECT COALESCE(SUM(LENGTH(log)), 0) FROM checks WHERE run_id = ? AND NOT (task_id = ? AND n = ? AND id = ? AND k = ?))
			+ (SELECT COALESCE(SUM(LENGTH(log)), 0) FROM prerequisites WHERE run_id = ?)`,
			t.runID, r.TaskID, r.N, r.ID, r.K, t.runID).Scan(&kept); err != nil {
			return err
		}
		if kept+int64(len(r.Log)) > maxRunLogs {
			r.Log, r.LogDropped = nil, true
		}
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO checks (run_id, task_id, n, id, k, argv, dir, subject, state, pid, pgroup, process_started_at, started_at, finished_at,
			exit, signal, timed_out, killed, stable, truncated, total, log, log_dropped, log_sha256, tail, phase, manifest, ports, tries, error, url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, task_id, n, id, k) DO UPDATE SET argv = excluded.argv, dir = excluded.dir, subject = excluded.subject, state = excluded.state,
			pid = excluded.pid, pgroup = excluded.pgroup, process_started_at = excluded.process_started_at, started_at = excluded.started_at,
			finished_at = excluded.finished_at, exit = excluded.exit, signal = excluded.signal, timed_out = excluded.timed_out, killed = excluded.killed,
			stable = excluded.stable, truncated = excluded.truncated, total = excluded.total, log = excluded.log, log_dropped = excluded.log_dropped,
			log_sha256 = excluded.log_sha256, tail = excluded.tail, phase = excluded.phase, manifest = excluded.manifest, ports = excluded.ports,
			tries = excluded.tries, error = excluded.error, url = excluded.url`,
		t.runID, r.TaskID, r.N, r.ID, r.K, argv, r.Dir, r.Subject, r.State, r.PID, r.Group, stamp(r.ProcessStartedAt), stamp(r.StartedAt), stamp(r.FinishedAt),
		r.Exit, r.Signal, b2i(r.TimedOut), b2i(r.Killed), b2i(r.Stable), b2i(r.Truncated), r.Total, r.Log, b2i(r.LogDropped), r.LogSHA256, r.Tail,
		phase, r.Manifest, ports, tries, r.Error, r.URL)
	return err
}

// PutPrerequisite puts a prerequisite's row, new or changed, as PutCheck
// puts a check's: its log at most MaxLog, and the run's logs at most
// MaxRunLogs in all, the checks' with the prerequisites'.
func (t *Tx) PutPrerequisite(r PrerequisiteRow) error {
	if len(r.Log) > MaxLog {
		return fmt.Errorf("%w: the log is %d bytes", ErrTooLarge, len(r.Log))
	}
	if err := rowText(r.URL, r.Manifest, r.Error); err != nil {
		return err
	}
	argv, err := encode(r.Argv, MaxPayload)
	if err != nil {
		return err
	}
	if r.LogDropped {
		r.Log = nil
	}
	if len(r.Log) > 0 {
		var kept int64
		if err := t.tx.QueryRowContext(t.ctx, `SELECT (SELECT COALESCE(SUM(LENGTH(log)), 0) FROM checks WHERE run_id = ?)
			+ (SELECT COALESCE(SUM(LENGTH(log)), 0) FROM prerequisites WHERE run_id = ? AND NOT (id = ? AND k = ?))`,
			t.runID, t.runID, r.ID, r.K).Scan(&kept); err != nil {
			return err
		}
		if kept+int64(len(r.Log)) > maxRunLogs {
			r.Log, r.LogDropped = nil, true
		}
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO prerequisites (run_id, id, k, argv, dir, subject, state, pid, pgroup, process_started_at, started_at, finished_at,
			exit, signal, timed_out, killed, stable, truncated, total, log, log_dropped, log_sha256, tail, manifest, error, url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, id, k) DO UPDATE SET argv = excluded.argv, dir = excluded.dir, subject = excluded.subject, state = excluded.state,
			pid = excluded.pid, pgroup = excluded.pgroup, process_started_at = excluded.process_started_at, started_at = excluded.started_at,
			finished_at = excluded.finished_at, exit = excluded.exit, signal = excluded.signal, timed_out = excluded.timed_out, killed = excluded.killed,
			stable = excluded.stable, truncated = excluded.truncated, total = excluded.total, log = excluded.log, log_dropped = excluded.log_dropped,
			log_sha256 = excluded.log_sha256, tail = excluded.tail, manifest = excluded.manifest, error = excluded.error, url = excluded.url`,
		t.runID, r.ID, r.K, argv, r.Dir, r.Subject, r.State, r.PID, r.Group, stamp(r.ProcessStartedAt), stamp(r.StartedAt), stamp(r.FinishedAt),
		r.Exit, r.Signal, b2i(r.TimedOut), b2i(r.Killed), b2i(r.Stable), b2i(r.Truncated), r.Total, r.Log, b2i(r.LogDropped), r.LogSHA256, r.Tail,
		r.Manifest, r.Error, r.URL)
	return err
}

// PutTree puts a tree's row, new or changed: resolved, ready or ended.
func (t *Tx) PutTree(r TreeRow) error {
	if err := rowText("", r.Manifest, r.Why, r.Preview); err != nil {
		return err
	}
	ports, err := encodePorts(r.Ports)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO trees (run_id, task_id, n, kind, dir, commit_id, manifest, ports, preview, state, why, interrupted, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, task_id, n, kind) DO UPDATE SET dir = excluded.dir, commit_id = excluded.commit_id, manifest = excluded.manifest,
			ports = excluded.ports, preview = excluded.preview, state = excluded.state, why = excluded.why, interrupted = excluded.interrupted,
			ended_at = excluded.ended_at`,
		t.runID, r.TaskID, r.N, r.Kind, r.Dir, r.Commit, r.Manifest, ports, r.Preview, r.State, r.Why, b2i(r.Interrupted), stamp(r.EndedAt))
	return err
}

// rowText refuses a receipt's or a tree's words past an event's bound,
// and a receipt's URL past MaxURL.
func rowText(url string, texts ...string) error {
	if len(url) > MaxURL {
		return fmt.Errorf("%w: the URL is %d bytes", ErrTooLarge, len(url))
	}
	for _, s := range texts {
		if len(s) > MaxPayload {
			return fmt.Errorf("%w: a row's text is %d bytes", ErrTooLarge, len(s))
		}
	}
	return nil
}

// encodePorts is a tree's ports as a row keeps them: a JSON object, name
// to number; "" for none.
func encodePorts(ports map[string]int) (string, error) {
	if len(ports) == 0 {
		return "", nil
	}
	return encode(ports, MaxPayload)
}

// decodePorts reads ports as encodePorts wrote them.
func decodePorts(raw string) (map[string]int, error) {
	if raw == "" {
		return nil, nil
	}
	var ports map[string]int
	if err := json.Unmarshal([]byte(raw), &ports); err != nil {
		return nil, err
	}
	return ports, nil
}

// encode marshals v as JSON within max bytes; nil is "".
func encode(v any, max int) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(b) > max {
		return "", fmt.Errorf("%w: %d bytes of JSON", ErrTooLarge, len(b))
	}
	return string(b), nil
}

// Readers. Each is a snapshot on the one connection, between transitions.

// Run reads a run's row.
func (l *Ledger) Run(ctx context.Context, id string) (RunRow, bool, error) {
	rows, err := l.runs(ctx, "WHERE id = ?", id)
	if err != nil || len(rows) == 0 {
		return RunRow{}, false, err
	}
	return rows[0], true, nil
}

// Runs reads every run's row, oldest first.
func (l *Ledger) Runs(ctx context.Context) ([]RunRow, error) {
	return l.runs(ctx, "ORDER BY created_at, id")
}

// runCols are the runs columns a RunRow is read from, in its scan order.
const runCols = `id, crew_id, crew, base, state, outcome, paused, revision, continued_from, continued_by, created_at, closed_at, paused_why, manifest, ticket, package`

func (l *Ledger) runs(ctx context.Context, where string, args ...any) ([]RunRow, error) {
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+runCols+` FROM runs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var r RunRow
		var paused int
		var created, closed string
		if err := rows.Scan(&r.ID, &r.CrewID, &r.Crew, &r.Base, &r.State, &r.Outcome, &paused, &r.Revision, &r.ContinuedFrom, &r.ContinuedBy, &created, &closed, &r.PausedWhy, &r.Manifest, &r.Ticket, &r.Package); err != nil {
			return nil, err
		}
		r.Paused, r.CreatedAt, r.ClosedAt = paused != 0, unstamp(created), unstamp(closed)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Tasks reads a run's tasks, in id order.
func (l *Ledger) Tasks(ctx context.Context, runID string) ([]TaskRow, error) {
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, member, kind, state, reason, attempts, result_attempt, result_commit, verdict, imported_from, repair, extra, withdrawn FROM tasks WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		r := TaskRow{RunID: runID}
		if err := rows.Scan(&r.ID, &r.Member, &r.Kind, &r.State, &r.Reason, &r.Attempts, &r.ResultAttempt, &r.ResultCommit, &r.Verdict, &r.ImportedFrom, &r.Repair, &r.Extra, &r.Withdrawn); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Attempts reads a run's attempts, by task and number.
func (l *Ledger) Attempts(ctx context.Context, runID string) ([]AttemptRow, error) {
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT task_id, n, member, session_id, worktree, branch, start_commit, result_commit, state, stage, error, reserved_at, launched_at, prompted_at, closed_at, result, checkpoint
		FROM attempts WHERE run_id = ? ORDER BY task_id, n`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttemptRow
	for rows.Next() {
		r := AttemptRow{RunID: runID}
		var reserved, launched, prompted, closed string
		if err := rows.Scan(&r.TaskID, &r.N, &r.Member, &r.SessionID, &r.Worktree, &r.Branch, &r.StartCommit, &r.ResultCommit, &r.State, &r.Stage, &r.Error, &reserved, &launched, &prompted, &closed, &r.Result, &r.Checkpoint); err != nil {
			return nil, err
		}
		r.ReservedAt, r.LaunchedAt, r.PromptedAt, r.ClosedAt = unstamp(reserved), unstamp(launched), unstamp(prompted), unstamp(closed)
		out = append(out, r)
	}
	return out, rows.Err()
}

// checkCols are the checks columns a CheckRow is read from, in its scan
// order, the log's own column (or NULL) last; prerequisiteCols the same of
// a prerequisite's row.
const (
	checkCols        = `task_id, n, id, k, argv, dir, subject, state, pid, pgroup, process_started_at, started_at, finished_at, exit, signal, timed_out, killed, stable, truncated, total, log_dropped, log_sha256, tail, phase, manifest, ports, tries, error, url, COALESCE(LENGTH(log), 0)`
	prerequisiteCols = `id, k, argv, dir, subject, state, pid, pgroup, process_started_at, started_at, finished_at, exit, signal, timed_out, killed, stable, truncated, total, log_dropped, log_sha256, tail, manifest, error, url, COALESCE(LENGTH(log), 0)`
)

// scanner is a row to scan: *sql.Row or *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// scanCheck reads a CheckRow as checkCols and its log's column give it.
func scanCheck(sc scanner, runID string) (CheckRow, error) {
	r := CheckRow{RunID: runID}
	var argv, pstarted, started, finished, ports string
	var timedOut, killed, stable, truncated, dropped int
	var log []byte
	if err := sc.Scan(&r.TaskID, &r.N, &r.ID, &r.K, &argv, &r.Dir, &r.Subject, &r.State, &r.PID, &r.Group, &pstarted, &started, &finished, &r.Exit, &r.Signal, &timedOut, &killed, &stable, &truncated, &r.Total,
		&dropped, &r.LogSHA256, &r.Tail, &r.Phase, &r.Manifest, &ports, &r.Tries, &r.Error, &r.URL, &r.LogBytes, &log); err != nil {
		return CheckRow{}, err
	}
	r.LogDropped = dropped != 0
	if argv != "" {
		if err := json.Unmarshal([]byte(argv), &r.Argv); err != nil {
			return CheckRow{}, err
		}
	}
	p, err := decodePorts(ports)
	if err != nil {
		return CheckRow{}, err
	}
	r.Ports = p
	r.ProcessStartedAt, r.StartedAt, r.FinishedAt = unstamp(pstarted), unstamp(started), unstamp(finished)
	r.TimedOut, r.Killed, r.Stable, r.Truncated = timedOut != 0, killed != 0, stable != 0, truncated != 0
	r.Log = log
	return r, nil
}

// scanPrerequisite reads a PrerequisiteRow as prerequisiteCols and its
// log's column give it.
func scanPrerequisite(sc scanner, runID string) (PrerequisiteRow, error) {
	r := PrerequisiteRow{RunID: runID}
	var argv, pstarted, started, finished string
	var timedOut, killed, stable, truncated, dropped int
	var log []byte
	if err := sc.Scan(&r.ID, &r.K, &argv, &r.Dir, &r.Subject, &r.State, &r.PID, &r.Group, &pstarted, &started, &finished, &r.Exit, &r.Signal, &timedOut, &killed, &stable, &truncated, &r.Total,
		&dropped, &r.LogSHA256, &r.Tail, &r.Manifest, &r.Error, &r.URL, &r.LogBytes, &log); err != nil {
		return PrerequisiteRow{}, err
	}
	r.LogDropped = dropped != 0
	if argv != "" {
		if err := json.Unmarshal([]byte(argv), &r.Argv); err != nil {
			return PrerequisiteRow{}, err
		}
	}
	r.ProcessStartedAt, r.StartedAt, r.FinishedAt = unstamp(pstarted), unstamp(started), unstamp(finished)
	r.TimedOut, r.Killed, r.Stable, r.Truncated = timedOut != 0, killed != 0, stable != 0, truncated != 0
	r.Log = log
	return r, nil
}

// logColumn is the log's column as a reader asks for it: the blob, or
// NULL when the logs do not come along.
func logColumn(withLog bool) string {
	if withLog {
		return "log"
	}
	return "NULL"
}

// Checks reads the checks of a run, or of one attempt when taskID is set
// (n then too), the steps of its trees among them, by task, attempt, id
// and k; withLog says whether the log blobs come along (LogBytes always
// does).
func (l *Ledger) Checks(ctx context.Context, runID, taskID string, n int, withLog bool) ([]CheckRow, error) {
	where, args := "WHERE run_id = ?", []any{runID}
	if taskID != "" {
		where, args = where+" AND task_id = ? AND n = ?", append(args, taskID, n)
	}
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+checkCols+`, `+logColumn(withLog)+` FROM checks `+where+` ORDER BY task_id, n, id, k`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckRow
	for rows.Next() {
		r, err := scanCheck(rows, runID)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Check reads one receipt of an attempt, its log with it; ok is false
// when there is no such row.
func (l *Ledger) Check(ctx context.Context, runID, taskID string, n int, id string, k int) (CheckRow, bool, error) {
	db, err := l.conn()
	if err != nil {
		return CheckRow{}, false, err
	}
	r, err := scanCheck(db.QueryRowContext(ctx, `SELECT `+checkCols+`, log FROM checks WHERE run_id = ? AND task_id = ? AND n = ? AND id = ? AND k = ?`, runID, taskID, n, id, k), runID)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckRow{}, false, nil
	}
	if err != nil {
		return CheckRow{}, false, err
	}
	return r, true, nil
}

// Prerequisites reads a run's prerequisites' receipts, by id and k;
// withLog says whether the log blobs come along.
func (l *Ledger) Prerequisites(ctx context.Context, runID string, withLog bool) ([]PrerequisiteRow, error) {
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+prerequisiteCols+`, `+logColumn(withLog)+` FROM prerequisites WHERE run_id = ? ORDER BY id, k`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrerequisiteRow
	for rows.Next() {
		r, err := scanPrerequisite(rows, runID)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Prerequisite reads one receipt of a run's prerequisite, its log with
// it; ok is false when there is no such row.
func (l *Ledger) Prerequisite(ctx context.Context, runID, id string, k int) (PrerequisiteRow, bool, error) {
	db, err := l.conn()
	if err != nil {
		return PrerequisiteRow{}, false, err
	}
	r, err := scanPrerequisite(db.QueryRowContext(ctx, `SELECT `+prerequisiteCols+`, log FROM prerequisites WHERE run_id = ? AND id = ? AND k = ?`, runID, id, k), runID)
	if errors.Is(err, sql.ErrNoRows) {
		return PrerequisiteRow{}, false, nil
	}
	if err != nil {
		return PrerequisiteRow{}, false, err
	}
	return r, true, nil
}

// Trees reads a run's trees, by task, attempt and kind.
func (l *Ledger) Trees(ctx context.Context, runID string) ([]TreeRow, error) {
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT task_id, n, kind, dir, commit_id, manifest, ports, preview, state, why, interrupted, ended_at FROM trees WHERE run_id = ? ORDER BY task_id, n, kind`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TreeRow
	for rows.Next() {
		r := TreeRow{RunID: runID}
		var ports, ended string
		var interrupted int
		if err := rows.Scan(&r.TaskID, &r.N, &r.Kind, &r.Dir, &r.Commit, &r.Manifest, &ports, &r.Preview, &r.State, &r.Why, &interrupted, &ended); err != nil {
			return nil, err
		}
		p, err := decodePorts(ports)
		if err != nil {
			return nil, err
		}
		r.Ports, r.Interrupted, r.EndedAt = p, interrupted != 0, unstamp(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Receipt reads a command's receipt.
func (l *Ledger) Receipt(ctx context.Context, runID, actor, id string) (CommandRow, bool, error) {
	db, err := l.conn()
	if err != nil {
		return CommandRow{}, false, err
	}
	r := CommandRow{RunID: runID, Actor: actor, ID: id}
	var at string
	err = db.QueryRowContext(ctx, "SELECT intent, fingerprint, status, receipt, created_at FROM commands WHERE run_id = ? AND actor = ? AND id = ?", runID, actor, id).
		Scan(&r.Intent, &r.Fingerprint, &r.Status, &r.Receipt, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandRow{}, false, nil
	}
	if err != nil {
		return CommandRow{}, false, err
	}
	r.CreatedAt = unstamp(at)
	return r, true, nil
}

// Commands reads a run's commands, oldest first.
func (l *Ledger) Commands(ctx context.Context, runID string) ([]CommandRow, error) {
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT actor, id, intent, fingerprint, status, receipt, created_at FROM commands WHERE run_id = ? ORDER BY created_at, actor, id", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommandRow
	for rows.Next() {
		r := CommandRow{RunID: runID}
		var at string
		if err := rows.Scan(&r.Actor, &r.ID, &r.Intent, &r.Fingerprint, &r.Status, &r.Receipt, &at); err != nil {
			return nil, err
		}
		r.CreatedAt = unstamp(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Events reads a run's events after seq, at most limit of them, in order.
func (l *Ledger) Events(ctx context.Context, runID string, after int64, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	db, err := l.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT seq, at, kind, payload FROM events WHERE run_id = ? AND seq > ? ORDER BY seq LIMIT ?", runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		r := EventRow{RunID: runID}
		var at string
		if err := rows.Scan(&r.Seq, &at, &r.Kind, &r.Payload); err != nil {
			return nil, err
		}
		r.At = unstamp(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Recovered is what Recover concluded for one run.
type Recovered struct {
	RunID         string
	Attempts      map[string]int // attempts concluded, by their new state
	Checks        int            // checks interrupted, a tree's steps and services among them
	Prerequisites int            // prerequisites interrupted
	Trees         int            // trees ended: interrupted before their end was recorded
	Commands      int            // commands left uncertain
	Interrupted   bool           // the run itself was running
	// Services describes each service row of the run the recovery
	// concluded (counted in Checks too): a process the owner started that
	// may outlive it, reported and never signalled. The recovered event
	// counts them alone (Services, Unconfirmed), and each has a
	// service.ended event of its own, so no payload grows with the rows.
	Services []RecoveredService `json:"-"`
}

// RecoveredService is a service a tree was running, or starting, when the
// owner stopped, as its row says: its task, attempt and tree (worktree or
// checkout; "" when its tree's row is not found), its id, and, once its
// start was confirmed (its row running), its process: pid, group and start
// time, which tell a later process with the same pid apart. One whose start
// was not confirmed (its row still started) may be running and cannot be
// named. Nothing of the row's argv, log or words: the row has them.
type RecoveredService struct {
	Task      string
	N         int
	Tree      string
	ID        string
	Pid       int
	Group     int
	StartedAt time.Time
	Confirmed bool
}

// recoveredEvent is the recovered event's payload: what Recover concluded,
// its services by count alone, those whose start was not confirmed among
// them (Unconfirmed).
type recoveredEvent struct {
	Recovered
	ServiceCount int `json:"Services"`
	Unconfirmed  int
}

// treeLeftOpen is why a tree the recovery ends was not ended by the owner.
const treeLeftOpen = "the owner stopped before the tree was ended"

// Recover concludes what the owner left open when it last stopped, for
// every run that was running or has a row left open (a stopped run's
// command accepted and never settled, say): an attempt reserved and not
// launched is uncertain (a worktree and a process may exist); one
// launched, running, submitting or verifying is interrupted (its session
// went with the server; a candidate verifying is verified again in a
// fresh checkout); a check started or running is interrupted (its process
// may be alive still, so it is reported, never killed); a command accepted
// is uncertain; a run running is interrupted, a run already stopped or
// finished keeps its state. A prerequisite started or running is
// interrupted as a check is, and a tree not ended (the owner stopped
// between its attempt's close and its end) is ended and interrupted, its
// open step rows with it, each with a `tree.ended` event of its own;
// nothing is run for it (its ports are nobody's now, its cleanup is not
// run). A service row started or running is interrupted as a check is and
// described (Recovered.Services), with a `service.ended` event of its
// own: a process this server started that outlived it is reported, never
// signalled, a pid after a restart being nobody's to signal. Every payload
// Recover writes is bounded by its shape, never by the rows. One
// `recovered` event per run touched says so. A second call finds nothing
// open and writes nothing.
func (l *Ledger) Recover(ctx context.Context) ([]Recovered, error) {
	if ro, why := l.ReadOnly(); ro {
		return nil, fmt.Errorf("%w: %s", ErrReadOnly, why)
	}
	runs, err := l.runs(ctx, `WHERE state = ?
		OR EXISTS (SELECT 1 FROM attempts a WHERE a.run_id = runs.id AND a.state IN (?, ?, ?, ?, ?))
		OR EXISTS (SELECT 1 FROM checks c WHERE c.run_id = runs.id AND c.state IN (?, ?))
		OR EXISTS (SELECT 1 FROM prerequisites p WHERE p.run_id = runs.id AND p.state IN (?, ?))
		OR EXISTS (SELECT 1 FROM trees tr WHERE tr.run_id = runs.id AND tr.state != ?)
		OR EXISTS (SELECT 1 FROM commands m WHERE m.run_id = runs.id AND m.status = ?)
		ORDER BY created_at, id`,
		RunRunning, AttemptReserved, AttemptLaunched, AttemptRunning, AttemptSubmitting, AttemptVerifying, CheckStarted, CheckRunning, CheckStarted, CheckRunning, TreeEnded, CommandAccepted)
	if err != nil {
		return nil, err
	}
	var out []Recovered
	for _, run := range runs {
		rec := Recovered{RunID: run.ID, Attempts: map[string]int{}, Interrupted: run.State == RunRunning}
		_, err := l.Transition(ctx, run.ID, func(t *Tx) error {
			now := stamp(t.now)
			for _, c := range []struct{ from, to, stage string }{
				{AttemptReserved, AttemptUncertain, "reserved: the owner stopped before the launch was known"},
				{AttemptLaunched, AttemptInterrupted, "launched: the owner stopped before the prompt was typed"},
				{AttemptRunning, AttemptInterrupted, "running: the owner stopped"},
				{AttemptSubmitting, AttemptInterrupted, "submitting: the owner stopped before the result was taken"},
				{AttemptVerifying, AttemptInterrupted, "verifying: the owner stopped; the candidate is verified again"},
			} {
				res, err := t.tx.ExecContext(ctx, "UPDATE attempts SET state = ?, stage = ?, closed_at = ? WHERE run_id = ? AND state = ?", c.to, c.stage, now, run.ID, c.from)
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				rec.Attempts[c.to] += int(n)
			}
			services, err := t.openServices()
			if err != nil {
				return err
			}
			rec.Services = services
			res, err := t.tx.ExecContext(ctx, "UPDATE checks SET state = ?, finished_at = ? WHERE run_id = ? AND state IN (?, ?)", CheckInterrupted, now, run.ID, CheckStarted, CheckRunning)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			rec.Checks = int(n)
			unconfirmed := 0
			for _, sv := range rec.Services {
				if !sv.Confirmed {
					unconfirmed++
				}
				if err := t.Event("service.ended", serviceEnded(sv)); err != nil {
					return err
				}
			}
			res, err = t.tx.ExecContext(ctx, "UPDATE prerequisites SET state = ?, finished_at = ? WHERE run_id = ? AND state IN (?, ?)", CheckInterrupted, now, run.ID, CheckStarted, CheckRunning)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			rec.Prerequisites = int(n)
			if rec.Trees, err = t.endTrees(now); err != nil {
				return err
			}
			res, err = t.tx.ExecContext(ctx, "UPDATE commands SET status = ? WHERE run_id = ? AND status = ?", CommandUncertain, run.ID, CommandAccepted)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			rec.Commands = int(n)
			if rec.Interrupted {
				if _, err := t.tx.ExecContext(ctx, "UPDATE runs SET state = ? WHERE id = ?", RunInterrupted, run.ID); err != nil {
					return err
				}
			}
			return t.Event("recovered", recoveredEvent{Recovered: rec, ServiceCount: len(rec.Services), Unconfirmed: unconfirmed})
		})
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// openServices is the service rows of the transaction's run still open
// (started or running), each described with its tree: the tree of its
// attempt whose directory holds the row's (a step runs in its tree's
// working directory, the crew's directory in it).
func (t *Tx) openServices() ([]RecoveredService, error) {
	type treeDir struct {
		task, kind, dir string
		n               int
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT task_id, n, kind, dir FROM trees WHERE run_id = ?", t.runID)
	if err != nil {
		return nil, err
	}
	var trees []treeDir
	for rows.Next() {
		var td treeDir
		if err := rows.Scan(&td.task, &td.n, &td.kind, &td.dir); err != nil {
			rows.Close()
			return nil, err
		}
		trees = append(trees, td)
	}
	if err := cmp.Or(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	rows, err = t.tx.QueryContext(t.ctx, "SELECT task_id, n, id, dir, state, pid, pgroup, process_started_at FROM checks WHERE run_id = ? AND phase = ? AND state IN (?, ?) ORDER BY task_id, n, id, k",
		t.runID, PhaseService, CheckStarted, CheckRunning)
	if err != nil {
		return nil, err
	}
	var out []RecoveredService
	for rows.Next() {
		var sv RecoveredService
		var dir, state, started string
		if err := rows.Scan(&sv.Task, &sv.N, &sv.ID, &dir, &state, &sv.Pid, &sv.Group, &started); err != nil {
			rows.Close()
			return nil, err
		}
		sv.StartedAt, sv.Confirmed = unstamp(started), state == CheckRunning
		if !sv.Confirmed {
			sv.Pid, sv.Group, sv.StartedAt = 0, 0, time.Time{}
		}
		for _, td := range trees {
			if td.task == sv.Task && td.n == sv.N && (dir == td.dir || strings.HasPrefix(dir, td.dir+"/")) {
				sv.Tree = td.kind
			}
		}
		out = append(out, sv)
	}
	return out, cmp.Or(rows.Err(), rows.Close())
}

// serviceEnded is the service.ended event the recovery writes for a
// service it concluded: a payload of a fixed shape, whatever the row.
func serviceEnded(sv RecoveredService) map[string]any {
	return map[string]any{"task": sv.Task, "attempt": sv.N, "tree": sv.Tree, "id": sv.ID, "pid": sv.Pid, "group": sv.Group, "startedAt": stamp(sv.StartedAt),
		"confirmed": sv.Confirmed, "interrupted": true, "recovered": true}
}

// endTrees ends the trees of the transaction's run not ended yet, as the
// recovery finds them: ended and interrupted, with why, each with a
// `tree.ended` event of a fixed shape. It returns how many.
func (t *Tx) endTrees(now string) (int, error) {
	type key struct {
		task, kind string
		n          int
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT task_id, n, kind FROM trees WHERE run_id = ? AND state != ? ORDER BY task_id, n, kind", t.runID, TreeEnded)
	if err != nil {
		return 0, err
	}
	var open []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.task, &k.n, &k.kind); err != nil {
			rows.Close()
			return 0, err
		}
		open = append(open, k)
	}
	if err := cmp.Or(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	if len(open) == 0 {
		return 0, nil
	}
	if _, err := t.tx.ExecContext(t.ctx, "UPDATE trees SET state = ?, interrupted = 1, why = ?, ended_at = ? WHERE run_id = ? AND state != ?", TreeEnded, treeLeftOpen, now, t.runID, TreeEnded); err != nil {
		return 0, err
	}
	for _, k := range open {
		if err := t.Event("tree.ended", map[string]any{"tree": k.kind, "task": k.task, "attempt": k.n, "why": treeLeftOpen, "interrupted": true, "recovered": true}); err != nil {
			return 0, err
		}
	}
	return len(open), nil
}

// ErrNotPrunable says a run's rows must stay: the run is open, a tree of
// it is still ending or a prerequisite of it still running, a
// continuation names it as its source, or another run imports one of its
// results.
var ErrNotPrunable = errors.New("ledger: the run is still needed")

// Prune deletes a run's rows, its tasks', attempts', checks',
// prerequisites', trees', commands' and events' with them, once the run
// is closed, every tree of it ended, no prerequisite of it running, and
// nothing cites it: a finished run can still be ending a tree, and its
// rows are what the end and the recovery write.
func (l *Ledger) Prune(ctx context.Context, runID string) error {
	if l.readOnly {
		return fmt.Errorf("%w: %s", ErrReadOnly, l.why)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, closed string
	switch err := tx.QueryRowContext(ctx, "SELECT state, closed_at FROM runs WHERE id = ?", runID).Scan(&state, &closed); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	if closed == "" || state == RunRunning {
		return fmt.Errorf("%w: it is %s", ErrNotPrunable, state)
	}
	var trees, prerequisites int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM trees WHERE run_id = ? AND state != ?), (SELECT COUNT(*) FROM prerequisites WHERE run_id = ? AND state IN (?, ?))`,
		runID, TreeEnded, runID, CheckStarted, CheckRunning).Scan(&trees, &prerequisites); err != nil {
		return err
	}
	switch {
	case trees > 0:
		return fmt.Errorf("%w: a tree of it is still ending", ErrNotPrunable)
	case prerequisites > 0:
		return fmt.Errorf("%w: a prerequisite of it is still running", ErrNotPrunable)
	}
	var cited int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runs WHERE continued_from = ?) + (SELECT COUNT(*) FROM tasks WHERE imported_from LIKE ?)`,
		runID, strings.ReplaceAll(strings.ReplaceAll(runID, "%", `\%`), "_", `\_`)+"/%").Scan(&cited); err != nil {
		return err
	}
	if cited > 0 {
		return fmt.Errorf("%w: %d runs cite it", ErrNotPrunable, cited)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM runs WHERE id = ?", runID); err != nil {
		return err
	}
	return tx.Commit()
}
