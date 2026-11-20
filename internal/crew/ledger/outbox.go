package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// decisionOutboxTable is the outcome outbox (version 14): one row for each
// decision record that ever carried a decision router's policy, written in
// the transaction that gives the record its final state (applied,
// superseded, cancelled, expired), kept until the router acknowledges it.
// seq orders the rows and is never reused (AUTOINCREMENT); decision is the
// record's n, with no foreign key, so a row outlives a run pruned since;
// outcome is the outcome as the server encodes it (JSON), self-contained,
// so nothing in it is read from another table when it is served; at is
// when it was written (RFC 3339, UTC). A record under the server's own
// policy alone writes no row, so a server with no router keeps none, and
// no row of an older table is rewritten to make it. The outcome's bound is
// its writer's; no CHECK holds it. Nothing writes or reads the table yet.
const decisionOutboxTable = `CREATE TABLE decision_outbox (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		decision INTEGER NOT NULL,
		outcome TEXT NOT NULL,
		at TEXT NOT NULL
	)`

// decisionOutboxIndex keeps one outbox row for a decision record. Its rule
// is part of what the table means, not a read's speed, so it is the
// version's statement, never an access path (accessPaths).
const decisionOutboxIndex = `CREATE UNIQUE INDEX decision_outbox_decision ON decision_outbox(decision)`

// MaxDecisionOutcome bounds an outbox row's outcome, as encoded.
const MaxDecisionOutcome = 24 << 10

// The bounds of the words an outcome keeps of its record: its question and
// its why, in bytes, cut where a character ends.
const (
	maxOutcomeQuestion = 500
	maxOutcomeWhy      = 200
)

// Outcome is an outbox row's outcome as the ledger writes it: the record's
// id, its final state, who decided it and how (By, Via; both empty when no
// response did), the decision that superseded it, why, when it closed, the
// router's policy it carried and when that was first written, and what the
// record says of itself, so that nothing of it is read from another table
// when it is served: a run pruned since loses nothing.
type Outcome struct {
	Decision     string          `json:"decision"`
	State        string          `json:"state"`
	By           string          `json:"by,omitempty"`
	Via          string          `json:"via,omitempty"`
	SupersededBy string          `json:"supersededBy,omitempty"`
	Why          string          `json:"why,omitempty"`
	ClosedAt     time.Time       `json:"closedAt"`
	RoutedAt     string          `json:"routedAt,omitempty"`
	Policy       json.RawMessage `json:"policy"`
	Record       OutcomeRecord   `json:"record"`
}

// OutcomeRecord is what an outcome keeps of its record.
type OutcomeRecord struct {
	Origin    string    `json:"origin"`
	Type      string    `json:"type"`
	Gate      string    `json:"gate,omitempty"`
	Ticket    string    `json:"ticket,omitempty"`
	Run       string    `json:"run"`
	Task      string    `json:"task,omitempty"`
	Attempt   int       `json:"attempt,omitempty"`
	Session   string    `json:"session,omitempty"`
	Question  string    `json:"question"`
	Options   []string  `json:"options,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// The ways a routed record's answer came (Outcome.Via): an eligible
// person's, the operator's override, or the server's own policy.
const (
	OutcomeEligible = "eligible"
	OutcomeOverride = "override"
	OutcomeLocal    = "local"
)

// cutWords is s cut where a character ends to at most n bytes.
func cutWords(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// outcome writes decision n's outbox row from its record as this
// transaction reads it, once: a record whose row is written already (an
// applied approval superseded later) keeps its first, unless again says
// the row is written again: a supersession's row that names no
// replacement yet takes the one naming it, in the transaction that
// superseded it; any other row stays.
func (t *Tx) outcome(n int64, again bool) error {
	rec, ok, err := t.Record(n)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoDecision, DecisionID(n))
	}
	p, err := policyOf(rec.Policy)
	if err != nil {
		return err
	}
	var req requestShape
	_ = json.Unmarshal([]byte(rec.Request), &req)
	o := Outcome{
		Decision: rec.ID(), State: rec.State, SupersededBy: rec.SupersededBy,
		Why:      cutWords(strings.Join(strings.Fields(rec.Why), " "), maxOutcomeWhy),
		ClosedAt: rec.ClosedAt, RoutedAt: p.RoutedAt, Policy: json.RawMessage(rec.Policy),
		Record: OutcomeRecord{
			Origin: rec.Origin, Type: rec.Type, Gate: rec.Gate, Ticket: rec.Ticket, Run: rec.CurrentRun, Task: rec.TaskID,
			Attempt: rec.Attempt, Session: rec.SessionID, Question: cutWords(req.Question, maxOutcomeQuestion), CreatedAt: rec.CreatedAt,
		},
	}
	if o.ClosedAt.IsZero() {
		o.ClosedAt = t.now
	}
	for _, opt := range req.Options {
		o.Record.Options = append(o.Record.Options, opt.Label)
	}
	for _, r := range rec.Responses {
		if r.Status != ResponseAccepted {
			continue
		}
		o.By, o.Via = decidedBy(r)
		break
	}
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if again {
		if err := checkObject("the outcome", string(b), MaxDecisionOutcome, false); err != nil {
			return err
		}
		_, err := t.tx.ExecContext(t.ctx, `INSERT INTO decision_outbox (decision, outcome, at) VALUES (?, ?, ?)
			ON CONFLICT(decision) DO UPDATE SET outcome = excluded.outcome
			WHERE json_extract(decision_outbox.outcome, '$.state') = 'superseded' AND json_extract(decision_outbox.outcome, '$.supersededBy') IS NULL`, n, string(b), stamp(t.now))
		return err
	}
	return t.Outcome(n, string(b))
}

// decidedBy is who decided a routed record by its accepted response r, and
// how: an identity's subject or the operator (the workbench token); an
// override, an eligible person's answer, or the server's own policy, as
// the response's receipt says.
func decidedBy(r ResponseRow) (by, via string) {
	var a authorityShape
	_ = json.Unmarshal([]byte(r.Authority), &a)
	by = "operator"
	if a.Via == ViaIdentity && a.Subject != "" {
		by = a.Subject
	}
	var rc struct {
		Override bool     `json:"override"`
		Groups   []string `json:"groups"`
	}
	_ = json.Unmarshal([]byte(r.Receipt), &rc)
	switch {
	case rc.Override:
		return by, OutcomeOverride
	case len(rc.Groups) > 0:
		return by, OutcomeEligible
	}
	return by, OutcomeLocal
}

// Outcome writes decision n's outbox row, outcome (one JSON object of at
// most MaxDecisionOutcome bytes), unless the decision has one already: one
// row a record, which leaves only by the router's acknowledgement.
func (t *Tx) Outcome(n int64, outcome string) error {
	if err := checkObject("the outcome", outcome, MaxDecisionOutcome, false); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO decision_outbox (decision, outcome, at) VALUES (?, ?, ?) ON CONFLICT(decision) DO NOTHING`, n, outcome, stamp(t.now))
	return err
}

// PutPolicy writes decision n's policy (the one writer of the column after
// the opening) in its canonical form, only while the decision is open and
// at rev: false otherwise, nothing written. A comparison's selection,
// which only the operator answers, takes none (ErrInvalidDecision); a
// policy not of its shape or past MaxDecisionPolicy is refused. Every other
// column stays; the rev and the history are the caller's, in the same
// transaction. So a decision's entries change only while it is open.
func (t *Tx) PutPolicy(n int64, policy string, rev int) (bool, error) {
	cur, err := t.mustDecision(n)
	if err != nil {
		return false, err
	}
	t.changed(n)
	if cur.Origin == OriginCompare {
		return false, invalid("%s is a comparison's selection, the operator's: it takes no router's policy", cur.ID())
	}
	canon, err := canonicalPolicy(policy)
	if err != nil {
		return false, err
	}
	if cur.State != DecisionOpen || cur.Rev != rev {
		return false, nil
	}
	_, err = t.tx.ExecContext(t.ctx, `UPDATE decisions SET policy = ? WHERE n = ?`, canon, n)
	return err == nil, err
}

// versionOutbox is the first schema version with decision_outbox: a reader
// of an older database finds no row.
const versionOutbox = 14

// OutboxRow is one row of the outcome outbox.
type OutboxRow struct {
	Seq      int64
	Decision int64
	Outcome  string
	At       time.Time
}

// Outcomes reads the outbox in seq order after after, at most limit rows
// (a limit below 1 reads none).
func (l *Ledger) Outcomes(ctx context.Context, after int64, limit int) ([]OutboxRow, error) {
	db, version, err := l.connAt()
	if err != nil {
		return nil, err
	}
	if version < versionOutbox || limit < 1 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT seq, decision, outcome, at FROM decision_outbox WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		var at string
		if err := rows.Scan(&r.Seq, &r.Decision, &r.Outcome, &at); err != nil {
			return nil, err
		}
		r.At = unstamp(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AckOutcomes deletes the outbox's rows up to through: the decision
// router's acknowledgement, the only way a row leaves. It answers how many
// it deleted.
func (l *Ledger) AckOutcomes(ctx context.Context, through int64) (int64, error) {
	if l.readOnly {
		return 0, fmt.Errorf("%w: %s", ErrReadOnly, l.why)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, ErrClosed
	}
	res, err := l.db.ExecContext(ctx, `DELETE FROM decision_outbox WHERE seq <= ?`, through)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// OutcomeOf is decision n's outbox row's outcome, ok false for none (a
// record that never carried a router's policy, or one acknowledged).
func (l *Ledger) OutcomeOf(ctx context.Context, n int64) (Outcome, bool, error) {
	db, version, err := l.connAt()
	if err != nil || version < versionOutbox {
		return Outcome{}, false, err
	}
	var raw string
	err = db.QueryRowContext(ctx, `SELECT outcome FROM decision_outbox WHERE decision = ?`, n).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Outcome{}, false, nil
	case err != nil:
		return Outcome{}, false, err
	}
	var o Outcome
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return Outcome{}, false, err
	}
	return o, true, nil
}

// OpenRecordsAfter reads the open decisions numbered after after, in
// number order, at most limit (OpenDecisionsAfter's numbers, over the open
// decisions' index), each whole; more says one followed. A record closed
// between the two reads is left out.
func (l *Ledger) OpenRecordsAfter(ctx context.Context, after int64, limit int) (recs []DecisionRecord, more bool, err error) {
	ns, more, err := l.OpenDecisionsAfter(ctx, after, limit)
	if err != nil {
		return nil, false, err
	}
	for _, n := range ns {
		rec, ok, err := l.Decision(ctx, n)
		if err != nil {
			return nil, false, err
		}
		if ok && rec.State == DecisionOpen {
			recs = append(recs, rec)
		}
	}
	return recs, more, nil
}
