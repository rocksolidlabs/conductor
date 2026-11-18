package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// runOriginTable is where a run was launched from (version 13), one row
// per run, removed with it: the launching provider's registered name,
// the launch's operation id (empty for none), the provider's reference
// and label, a Dispatch project, the subject and its link, an earlier run
// of the same provider, the start commit asked for, the instant the run
// is stopped and the limit it was given in seconds, the digest the launch
// was held to and the agents' selections (JSON). No row of an older
// table is rewritten to make it, and no run before this version has a
// row. The columns' bounds are their writer's (checkOrigin); no CHECK
// holds them.
const runOriginTable = `CREATE TABLE run_origin (
		run_id TEXT NOT NULL PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
		provider TEXT NOT NULL,
		op_id TEXT NOT NULL DEFAULT '',
		ref TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		subject_url TEXT NOT NULL DEFAULT '',
		prior TEXT NOT NULL DEFAULT '',
		start_commit TEXT NOT NULL DEFAULT '',
		deadline TEXT NOT NULL DEFAULT '',
		limit_seconds INTEGER NOT NULL DEFAULT 0,
		resolved_digest TEXT NOT NULL DEFAULT '',
		agents TEXT NOT NULL DEFAULT ''
	)`

// runOriginOpIndex keeps one run per provider's operation id: rows with
// an empty operation id are not keyed, so any number of a provider's runs
// may have none. Its rule is part of what the table means, not a read's
// speed, so it is the version's statement, never an access path
// (accessPaths).
const runOriginOpIndex = `CREATE UNIQUE INDEX run_origin_op ON run_origin(provider, op_id) WHERE op_id != ''`

// versionOrigin is the first schema version with run_origin: a reader of
// an older database (a read-only ledger another server owns) finds no
// row, as no run before it has one.
const versionOrigin = 13

// The bounds of a run's origin row (checkOrigin), in bytes unless said.
const (
	MaxOriginProvider = 40
	MaxOriginOpID     = 32
	MaxOriginRef      = 200
	MaxOriginLabel    = 60 // characters
	MaxOriginSubject  = 512
	MaxOriginURL      = 2048
	MaxOriginRunID    = 73
	MaxOriginAgents   = 2048
	MinOriginLimit    = 600   // seconds
	MaxOriginLimit    = 86400 // seconds
)

// Errors of the origin's writer.
var (
	// ErrBadOrigin says an origin row is outside its bounds: nothing of it
	// is written.
	ErrBadOrigin = errors.New("ledger: the run's origin is outside its bounds")
	// ErrOpExists says another run's origin holds the provider's
	// operation id already: one run per provider's operation id.
	ErrOpExists = errors.New("ledger: the provider's operation id names another run")
	// ErrOriginExists says the run has an origin row already: a row is
	// written once and never changed.
	ErrOriginExists = errors.New("ledger: the run's origin is written already")
)

// OriginRow is where a run was launched from, as run_origin keeps it:
// the provider that launched it (or whose run a continuation follows),
// the launch's operation id ("" on a continuation's row), the provider's
// reference and label, the Dispatch project, the subject's reference,
// link and earlier run, the start commit asked for, the instant the run
// is stopped and its limit in seconds (zero for none), the resolved
// launch's digest and the agents' selections it confirmed (JSON).
type OriginRow struct {
	RunID          string
	Provider       string
	OpID           string
	Ref            string
	Label          string
	Project        string
	Subject        string
	SubjectURL     string
	Prior          string
	StartCommit    string
	Deadline       time.Time
	LimitSeconds   int
	ResolvedDigest string
	Agents         string
}

// PutOrigin writes the transaction's run's origin, once: a row of another
// run, or one outside its bounds, is refused (ErrBadOrigin), a second row
// of the run ErrOriginExists, and an operation id the provider's origin
// rows hold already ErrOpExists. Nothing is written by a refusal. The run's
// row must be written first.
func (t *Tx) PutOrigin(r OriginRow) error {
	if r.RunID != t.runID {
		return fmt.Errorf("%w: run %s's origin put in %s's transition", ErrBadOrigin, r.RunID, t.runID)
	}
	if err := checkOrigin(r); err != nil {
		return err
	}
	var n int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM run_origin WHERE run_id = ?", r.RunID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %s", ErrOriginExists, r.RunID)
	}
	if r.OpID != "" {
		var other string
		switch err := t.tx.QueryRowContext(t.ctx, "SELECT run_id FROM run_origin WHERE provider = ? AND op_id = ?", r.Provider, r.OpID).Scan(&other); {
		case err == nil:
			return fmt.Errorf("%w: %s", ErrOpExists, other)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO run_origin (`+originCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.Provider, r.OpID, r.Ref, r.Label, r.Project, r.Subject, r.SubjectURL, r.Prior, r.StartCommit, stamp(r.Deadline), r.LimitSeconds, r.ResolvedDigest, r.Agents)
	return err
}

// originCols are run_origin's columns, in OriginRow's order.
const originCols = `run_id, provider, op_id, ref, label, project, subject, subject_url, prior, start_commit, deadline, limit_seconds, resolved_digest, agents`

// checkOrigin holds r to its bounds: a provider's name of 1 to
// MaxOriginProvider of a-z, 0-9 and '-'; an operation id empty or of 1 to
// MaxOriginOpID of letters, digits and ._~-; a reference of 1 to
// MaxOriginRef bytes of printable ASCII with no space; a label of 1 to
// MaxOriginLabel characters with no control character; a project's key
// or none; a subject of at most MaxOriginSubject bytes on one line; a
// link, when given, https of at most MaxOriginURL printable bytes with no
// query or fragment; an earlier run of a run id's shape or none; a start
// commit and a digest of their hex lengths or none; a deadline and a
// limit given together (the limit MinOriginLimit to MaxOriginLimit
// seconds) or neither; agents of at most MaxOriginAgents bytes of JSON.
func checkOrigin(r OriginRow) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrBadOrigin, fmt.Sprintf(format, args...))
	}
	switch {
	case !originSlug(r.Provider, MaxOriginProvider):
		return bad("the provider %q is not a provider's name", r.Provider)
	case r.OpID != "" && !originOpID(r.OpID):
		return bad("the operation id is not 1 to %d of letters, digits and ._~-", MaxOriginOpID)
	case r.Ref == "" || len(r.Ref) > MaxOriginRef || !originPrintable(r.Ref, false):
		return bad("the reference is not 1 to %d bytes of printable ASCII with no space", MaxOriginRef)
	case r.Label == "" || utf8.RuneCountInString(r.Label) > MaxOriginLabel || !utf8.ValidString(r.Label) || strings.ContainsFunc(r.Label, unicode.IsControl):
		return bad("the label is not 1 to %d characters on one line", MaxOriginLabel)
	case r.Project != "" && !originProject(r.Project):
		return bad("the project %q is not a project's key", r.Project)
	case len(r.Subject) > MaxOriginSubject || !utf8.ValidString(r.Subject) || strings.ContainsFunc(r.Subject, unicode.IsControl):
		return bad("the subject is not at most %d bytes on one line", MaxOriginSubject)
	case r.SubjectURL != "" && (len(r.SubjectURL) > MaxOriginURL || !originPrintable(r.SubjectURL, false) || !strings.HasPrefix(r.SubjectURL, "https://") || strings.ContainsAny(r.SubjectURL, "?#")):
		return bad("the subject's link is not an https link of at most %d bytes with no query or fragment", MaxOriginURL)
	case r.Prior != "" && !originRunID(r.Prior):
		return bad("the earlier run %q is not a run's id", r.Prior)
	case r.StartCommit != "" && !originHex(r.StartCommit, 40) && !originHex(r.StartCommit, 64):
		return bad("the start commit is not a full commit id")
	case r.ResolvedDigest != "" && !originHex(r.ResolvedDigest, 64):
		return bad("the digest is not 64 lower-case hex")
	case r.Deadline.IsZero() != (r.LimitSeconds == 0):
		return bad("a deadline and a limit are given together, or neither")
	case r.LimitSeconds != 0 && (r.LimitSeconds < MinOriginLimit || r.LimitSeconds > MaxOriginLimit):
		return bad("the limit is not %d to %d seconds", MinOriginLimit, MaxOriginLimit)
	case len(r.Agents) > MaxOriginAgents || r.Agents != "" && !json.Valid([]byte(r.Agents)):
		return bad("the agents' selections are not at most %d bytes of JSON", MaxOriginAgents)
	}
	return nil
}

// originSlug reports whether s is 1 to max of a-z, 0-9 and '-', beginning
// with a letter or a digit.
func originSlug(s string, max int) bool {
	if s == "" || len(s) > max || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// originOpID reports whether s is 1 to MaxOriginOpID of letters, digits
// and ._~-.
func originOpID(s string) bool {
	if s == "" || len(s) > MaxOriginOpID {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '~' || c == '-':
		default:
			return false
		}
	}
	return true
}

// originPrintable reports whether s is printable ASCII, a space only when
// space says so.
func originPrintable(s string, space bool) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c > '~' || c == ' ' && !space {
			return false
		}
	}
	return true
}

// originProject reports whether s is a Dispatch project's key: an
// upper-case letter, then 1 to 5 upper-case letters or digits.
func originProject(s string) bool {
	if len(s) < 2 || len(s) > 6 || s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// originRunID reports whether s has a run id's shape: a crew id, a dash
// and 8 lower-case hex, at most MaxOriginRunID bytes.
func originRunID(s string) bool {
	i := strings.LastIndexByte(s, '-')
	return len(s) <= MaxOriginRunID && i > 0 && originHex(s[i+1:], 8) && originSlug(s[:i], MaxOriginRunID-9)
}

// originHex reports whether s is n lower-case hex digits.
func originHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Origin reads a run's origin; ok is false for a run with none (a person's
// run), and for every run of a database before versionOrigin.
func (l *Ledger) Origin(ctx context.Context, runID string) (OriginRow, bool, error) {
	return l.origin(ctx, "run_id = ?", runID)
}

// RunByOp reads the origin of the run a provider's launch with the
// operation id opID made; ok is false for none, and for an empty opID (a
// continuation's row names none). A read: it is answered by a read-only
// ledger too, and writes nothing.
func (l *Ledger) RunByOp(ctx context.Context, provider, opID string) (OriginRow, bool, error) {
	if opID == "" {
		return OriginRow{}, false, nil
	}
	return l.origin(ctx, "provider = ? AND op_id = ?", provider, opID)
}

func (l *Ledger) origin(ctx context.Context, where string, args ...any) (OriginRow, bool, error) {
	db, version, err := l.connAt()
	if err != nil {
		return OriginRow{}, false, err
	}
	if version < versionOrigin {
		return OriginRow{}, false, nil // no table before it, and so no origin
	}
	var r OriginRow
	var deadline string
	err = db.QueryRowContext(ctx, `SELECT `+originCols+` FROM run_origin WHERE `+where, args...).Scan(
		&r.RunID, &r.Provider, &r.OpID, &r.Ref, &r.Label, &r.Project, &r.Subject, &r.SubjectURL, &r.Prior, &r.StartCommit, &deadline, &r.LimitSeconds, &r.ResolvedDigest, &r.Agents)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return OriginRow{}, false, nil
	case err != nil:
		return OriginRow{}, false, err
	}
	r.Deadline = unstamp(deadline)
	return r, true, nil
}
