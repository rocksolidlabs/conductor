package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The links' reads (Program item IN1a, docs/plans/tracker-linking.md,
// "Links' reads"): the last read of the outside item a live delivery or
// external link names, a pull request's merge and its checks at its head
// commit among it, kept in link_reads, a cache of the API's reads. It is
// derived and local: no changes row, no rev, no receipt, never replicated
// (rules.go, ClassDerived); a read is written in a transaction of its own
// outside Apply (RecordRead), dropped when it is older by the system's
// clock than the one kept (stale_read) or when its link is gone, and a
// link removed drops its read in the remove's transaction. A read is
// announced to OnRead's listeners, never to OnChange's: it adds no changes
// row, so it has no place in the log's sequence.

// ReadNotice is what OnRead's listeners are told of a read: the ticket (its
// id) and its project (its key), the link (its id, "" for the ticket's
// linked issue) and when it was read. It carries no sequence.
type ReadNotice struct {
	Ticket  string    `json:"ticket"`
	Project string    `json:"project"`
	Link    string    `json:"link,omitempty"`
	ReadAt  time.Time `json:"readAt"`
}

// OnRead registers fn, called once after every read a store records
// (RecordRead, RecordReadError, RecordTicketRead, RecordTicketReadError),
// on the recording goroutine and outside the store's lock, as OnChange's
// listeners are called after a commit.
func (s *Store) OnRead(fn func(ReadNotice)) {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	s.readers = append(s.readers, fn)
}

// told tells OnRead's listeners of a read.
func (s *Store) told(n ReadNotice) {
	s.rmu.Lock()
	fns := slices.Clone(s.readers)
	s.rmu.Unlock()
	for _, fn := range fns {
		fn(n)
	}
}

// LinkCheck is one check of a pull request at its head commit as a link's
// read keeps it: its id (the system's, opaque), name, status and
// conclusion as the system words them, its page, the app that made it,
// whether it has a log, and when it completed.
type LinkCheck struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion,omitempty"`
	URL         string    `json:"url,omitempty"`
	App         string    `json:"app,omitempty"`
	HasLog      bool      `json:"hasLog,omitempty"`
	CompletedAt time.Time `json:"completedAt,omitzero"`
}

// ChecksIncomplete is why a link's checks are not every check the system
// has at the commit: the words of each reason (a kind of check that could
// not be read, each bound the read met), and the checks the system's
// totals counted that the set does not hold. A set with any is never "all
// passed", wherever it is shown.
type ChecksIncomplete struct {
	Words   []string `json:"words"`
	Omitted int      `json:"omitted,omitempty"`
}

// LinkRead is a read of the outside item a link names, as the API hands it
// to RecordRead: the item's system, host, kind and node id, its state, a
// pull request's merge (when and its commit) and head commit, its checks at
// the head commit and why they are not whole (nil when they are), its
// update time by the system's clock, and the read's validators. A read's
// time is the store's.
type LinkRead struct {
	System     string            `json:"system"`
	Host       string            `json:"host"`
	Kind       string            `json:"kind"`
	NodeID     string            `json:"nodeId"`
	State      string            `json:"state"`
	Merged     bool              `json:"merged"`
	MergedAt   time.Time         `json:"mergedAt,omitzero"`
	MergeSha   string            `json:"mergeSha,omitempty"`
	HeadSha    string            `json:"headSha,omitempty"`
	Checks     []LinkCheck       `json:"checks"`
	Incomplete *ChecksIncomplete `json:"incomplete,omitempty"`
	UpdatedAt  time.Time         `json:"updatedAt"`
	ReadAt     time.Time         `json:"readAt,omitzero"`
	IssueETag  string            `json:"issueEtag,omitempty"`
	PullETag   string            `json:"pullEtag,omitempty"`
	Error      string            `json:"error,omitempty"`
	ErrorAt    time.Time         `json:"errorAt,omitzero"`
}

// LinkReadSummary is a link's read as a ticket's read carries it, at most
// MaxLinkReadSummary at its escaped worst: the item's state and merge, its
// head commit, its checks counted (passed, failed, pending, other, whether
// the set is incomplete and how many the system counted that it does not
// hold), the system's update time, when it was read, and the last read's
// failure. The checks themselves are the link's own read's (LinkRead).
type LinkReadSummary struct {
	State     string        `json:"state,omitempty"`
	Merged    bool          `json:"merged"`
	MergedAt  time.Time     `json:"mergedAt,omitzero"`
	MergeSha  string        `json:"mergeSha,omitempty"`
	HeadSha   string        `json:"headSha,omitempty"`
	Checks    ChecksSummary `json:"checks"`
	UpdatedAt time.Time     `json:"updatedAt,omitzero"`
	ReadAt    time.Time     `json:"readAt,omitzero"`
	Error     string        `json:"error,omitempty"`
}

// ChecksSummary is a link's checks counted: passed (a success), failed (a
// failure, a timeout, a cancel, an action required, a start that failed,
// an error), pending (queued, in progress, waiting, or no conclusion yet)
// and other (neutral, skipped, stale and the rest); incomplete when the set
// is not whole, never all passed then; omitted, the checks the system
// counted that the set does not hold.
type ChecksSummary struct {
	Passed     int  `json:"passed"`
	Failed     int  `json:"failed"`
	Pending    int  `json:"pending"`
	Other      int  `json:"other"`
	Incomplete bool `json:"incomplete"`
	Omitted    int  `json:"omitted"`
}

// classify is a check's place in its summary.
func (c LinkCheck) classify() string {
	switch c.Conclusion {
	case "success":
		return "passed"
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "error":
		return "failed"
	case "":
		return "pending"
	}
	return "other"
}

// summary is the read as a ticket's read carries it.
func (r LinkRead) summary() LinkReadSummary {
	s := LinkReadSummary{State: r.State, Merged: r.Merged, MergedAt: r.MergedAt, MergeSha: r.MergeSha, HeadSha: r.HeadSha,
		UpdatedAt: r.UpdatedAt, ReadAt: r.ReadAt, Error: r.Error}
	for _, c := range r.Checks {
		switch c.classify() {
		case "passed":
			s.Checks.Passed++
		case "failed":
			s.Checks.Failed++
		case "pending":
			s.Checks.Pending++
		default:
			s.Checks.Other++
		}
	}
	if r.Incomplete != nil {
		s.Checks.Incomplete, s.Checks.Omitted = true, r.Incomplete.Omitted
	}
	return s
}

// Storable is a text from outside as the store keeps it, the one maker of
// one: valid UTF-8, every control character dropped (\n and \t kept when
// lines), a text of one line (not lines) also without the nine characters
// that reorder a line (U+202A to U+202E, U+2066 to U+2069), cut to n
// bytes at a character's edge. checkText accepts whatever it returns of
// one line; a text of lines keeps those nine, shown as written.
func Storable(s string, n int, lines bool) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r) && !(lines && (r == '\n' || r == '\t')):
			return -1
		case !lines && (r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069):
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	return cutBytes(s, n)
}

// checkText checks one of a read's texts: valid UTF-8 with no control
// character, at most n bytes (invalid, bound).
func checkText(what, s string, n int) error {
	switch {
	case len(s) > n:
		return refuse(RefusalBound, "%s is more than %d bytes", what, n)
	case !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return refuse(RefusalInvalid, "%s holds a control character or is not UTF-8", what)
	}
	return nil
}

// encodeLinkRead checks a link's read and gives its checks and their
// incompleteness as their columns hold them: the identity's parts as a
// snapshot's, a state of the system's, full commit ids, at most
// MaxLinkChecks checks of their shapes in at most MaxLinkChecksJSON, at
// most MaxIncompleteWords reasons of at most MaxIncompleteWord bytes in
// at most MaxChecksIncomplete (bound, invalid).
func encodeLinkRead(r LinkRead) (checks, incomplete string, err error) {
	switch {
	case !SystemPattern.MatchString(r.System):
		return "", "", refuse(RefusalInvalid, "the system %q is not a tracker system's name", r.System)
	case len(r.Host) > MaxExternalHost || !hostPattern.MatchString(r.Host):
		return "", "", refuse(RefusalInvalid, "the host %q is not a lower-case host name", r.Host)
	case !nodeIDPattern.MatchString(r.NodeID):
		return "", "", refuse(RefusalInvalid, "the item's node id is not 1 to %d letters, digits, '_', '=' or '-'", MaxNodeID)
	case r.Kind != ExternalIssue && r.Kind != ExternalChange:
		return "", "", refuse(RefusalInvalid, "the item's kind %q is neither issue nor change", r.Kind)
	case !slices.Contains(externalStates, r.State):
		return "", "", refuse(RefusalInvalid, "the item's state %q is none of open, closed and merged", r.State)
	case r.UpdatedAt.IsZero():
		return "", "", refuse(RefusalInvalid, "the read names no time the item was updated")
	case r.Merged != (r.State == "merged"):
		return "", "", refuse(RefusalInvalid, "an item is merged exactly when its state is merged")
	case !validator(r.IssueETag) || !validator(r.PullETag):
		return "", "", refuse(RefusalInvalid, "a read's validator is not at most %d bytes of printable ASCII", MaxValidator)
	case len(r.Checks) > MaxLinkChecks:
		return "", "", refuse(RefusalBound, "at most %d checks in a read; this has %d", MaxLinkChecks, len(r.Checks))
	}
	for _, sha := range []struct{ what, v string }{{"the merge commit", r.MergeSha}, {"the head commit", r.HeadSha}} {
		if sha.v != "" && !objectIDPattern.MatchString(sha.v) {
			return "", "", refuse(RefusalInvalid, "%s is not a full commit id", sha.what)
		}
	}
	// A copy: the caller's read, which two calls may share, is never
	// changed under it.
	list := slices.Clone(r.Checks)
	if list == nil {
		list = []LinkCheck{}
	}
	for i, c := range list {
		if err := checkRemoteID("a check's id", c.ID); err != nil || c.ID == "" {
			return "", "", refuse(RefusalInvalid, "check %d's id is not 1 to %d bytes of printable ASCII", i, MaxRemoteID)
		}
		for _, f := range []struct {
			what, v string
			n       int
		}{{"a check's name", c.Name, MaxCheckName}, {"a check's app", c.App, MaxCheckName}, {"a check's status", c.Status, 40}, {"a check's conclusion", c.Conclusion, 40}} {
			if err := checkText(f.what, f.v, f.n); err != nil {
				return "", "", err
			}
		}
		// A state is the system's word, never an address; a name is words,
		// kept address-free (addressFree), within its bound.
		if holdsAddress(c.Status) || holdsAddress(c.Conclusion) {
			return "", "", refuse(RefusalInvalid, "check %d's state holds an address", i)
		}
		list[i].Name, list[i].App = cutBytes(addressFree(c.Name), MaxCheckName), cutBytes(addressFree(c.App), MaxCheckName)
		if c.URL != "" {
			// A check's page is kept only whole: one the store does not
			// admit, or that carries a query, a fragment or a user, is not
			// kept, the check kept without it.
			a, err := admitAddress("a check's page", c.URL)
			list[i].URL = a.kept
			if err != nil || a.carries() {
				list[i].URL = ""
			}
		}
		list[i].CompletedAt = c.CompletedAt.UTC()
	}
	if checks, err = encodeValue(list); err != nil {
		return "", "", err
	}
	if len(checks) > MaxLinkChecksJSON {
		return "", "", refuse(RefusalBound, "the read's checks are %d bytes, more than %d", len(checks), MaxLinkChecksJSON)
	}
	if r.Incomplete != nil {
		// A copy, its words address-free (addressFree) within their bound:
		// the caller's read is never changed under it.
		in := ChecksIncomplete{Words: slices.Clone(r.Incomplete.Words), Omitted: r.Incomplete.Omitted}
		if len(in.Words) == 0 || len(in.Words) > MaxIncompleteWords || in.Omitted < 0 {
			return "", "", refuse(RefusalInvalid, "an incomplete set of checks says why in 1 to %d reasons", MaxIncompleteWords)
		}
		for i, w := range in.Words {
			if w == "" {
				return "", "", refuse(RefusalInvalid, "a reason the checks are incomplete is empty")
			}
			if err := checkText("a reason the checks are incomplete", w, MaxIncompleteWord); err != nil {
				return "", "", err
			}
			in.Words[i] = cutBytes(addressFree(w), MaxIncompleteWord)
		}
		if incomplete, err = encodeValue(in); err != nil {
			return "", "", err
		}
		if len(incomplete) > MaxChecksIncomplete {
			return "", "", refuse(RefusalBound, "why the checks are incomplete is %d bytes, more than %d", len(incomplete), MaxChecksIncomplete)
		}
	}
	return checks, incomplete, nil
}

// RecordRead records a read of the outside item the link with the id
// names, in its link_reads row, in a transaction of its own outside Apply
// (no changes row, no receipt): the link and its ticket must be live (a
// *Refusal not_found otherwise: a read for a link since removed, or a
// ticket since deleted, is dropped) and a
// delivery or an external link (invalid); the read checked and bounded as
// encodeLinkRead says, on a copy (r is never changed); a read of another
// item than the one the link was first read as (its system, host and node
// id) is a *Refusal invalid, and one older by the system's clock than the
// one kept a *Refusal stale_read, nothing written either way. Then OnRead's
// listeners are told.
func (s *Store) RecordRead(ctx context.Context, linkID string, r LinkRead) error {
	checks, incomplete, err := encodeLinkRead(r)
	if err != nil {
		return err
	}
	return s.recordLinkRead(ctx, linkID, func(dbtx *sql.Tx, l Link, now time.Time) error {
		// The item the link was first read as is the link's: a read of
		// another (a node id, a host or a system of its own) is refused, as
		// is one older than the read kept. A row a failure alone wrote names
		// no item yet.
		var system, host, node, at string
		switch err := dbtx.QueryRowContext(ctx, "SELECT system, host, node_id, updated_at FROM link_reads WHERE link_id = ?", l.ID).Scan(&system, &host, &node, &at); {
		case isNoRows(err), node == "":
		case err != nil:
			return err
		case system != r.System || host != r.Host || node != r.NodeID:
			return refuse(RefusalInvalid, "the read is of %s, not %s, the item the link was read as", ExternalIdentity(r.System, r.Host, r.NodeID), ExternalIdentity(system, host, node))
		case at != "" && r.UpdatedAt.Before(unstamp(at)):
			return refuse(RefusalStaleRead, "the item was read as updated at %s, which is older than the read kept (%s)", stamp(r.UpdatedAt), at)
		}
		merged := 0
		if r.Merged {
			merged = 1
		}
		_, err := dbtx.ExecContext(ctx, `INSERT INTO link_reads (link_id, ticket_id, system, host, kind, node_id, state, merged, merged_at, merge_sha, head_sha,
			checks, checks_incomplete, updated_at, read_at, issue_etag, pull_etag, error, error_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '')
			ON CONFLICT (link_id) DO UPDATE SET system = excluded.system, host = excluded.host, kind = excluded.kind, node_id = excluded.node_id,
			state = excluded.state, merged = excluded.merged, merged_at = excluded.merged_at, merge_sha = excluded.merge_sha, head_sha = excluded.head_sha,
			checks = excluded.checks, checks_incomplete = excluded.checks_incomplete, updated_at = excluded.updated_at, read_at = excluded.read_at,
			issue_etag = excluded.issue_etag, pull_etag = excluded.pull_etag, error = '', error_at = ''`,
			l.ID, l.TicketID, r.System, r.Host, r.Kind, r.NodeID, r.State, merged, stamp(r.MergedAt), r.MergeSha, r.HeadSha, checks, incomplete,
			stamp(r.UpdatedAt), stamp(now), r.IssueETag, r.PullETag)
		return err
	})
}

// RecordReadError records a read of the link's item that failed, its words
// in the link's link_reads row (cut to MaxReadError characters), the read
// kept otherwise as it was, as RecordRead records a read.
func (s *Store) RecordReadError(ctx context.Context, linkID, words string) error {
	return s.recordLinkRead(ctx, linkID, func(dbtx *sql.Tx, l Link, now time.Time) error {
		_, err := dbtx.ExecContext(ctx, `INSERT INTO link_reads (link_id, ticket_id, error, error_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (link_id) DO UPDATE SET error = excluded.error, error_at = excluded.error_at`, l.ID, l.TicketID, readWords(words), stamp(now))
		return err
	})
}

// recordLinkRead runs write on the live delivery or external link with the
// id in a transaction of its own under the store's lock, then tells
// OnRead's listeners.
func (s *Store) recordLinkRead(ctx context.Context, linkID string, write func(*sql.Tx, Link, time.Time) error) error {
	var notice ReadNotice
	err := s.derived(ctx, func(dbtx *sql.Tx, now time.Time) error {
		l, err := scanLink(dbtx.QueryRowContext(ctx, "SELECT "+linkColumns+" FROM links WHERE id = ?", linkID))
		switch {
		case isNoRows(err):
			return refuse(RefusalNotFound, "no link %s", linkID)
		case err != nil:
			return err
		case !l.DeletedAt.IsZero():
			return refuse(RefusalNotFound, "the link %s is removed: its read is dropped", linkID)
		case l.Kind != LinkDelivery && l.Kind != LinkExternal:
			return refuse(RefusalInvalid, "a %s link names no outside item to read", l.Kind)
		}
		tk, err := ticketByID(ctx, dbtx, l.TicketID)
		switch {
		case err != nil:
			return err
		case !tk.DeletedAt.IsZero():
			return refuse(RefusalNotFound, "%s is deleted: the read is dropped", tk.Key)
		}
		if err := write(dbtx, l, now); err != nil {
			return err
		}
		p, err := projectByID(ctx, dbtx, tk.ProjectID)
		if err != nil {
			return err
		}
		notice = ReadNotice{Ticket: tk.ID, Project: p.Key, Link: l.ID, ReadAt: now}
		return nil
	})
	if err != nil {
		return err
	}
	s.told(notice)
	return nil
}

// linkReadColumns is a link's read as scanLinkRead reads it, the link's id
// first.
const linkReadColumns = `link_id, system, host, kind, node_id, state, merged, merged_at, merge_sha, head_sha, checks, checks_incomplete, updated_at,
	read_at, issue_etag, pull_etag, error, error_at`

// scanLinkRead reads a link's read and its link's id.
func scanLinkRead(row interface{ Scan(...any) error }) (string, LinkRead, error) {
	var id, mergedAt, checks, incomplete, updated, read, errAt string
	var merged int
	var r LinkRead
	if err := row.Scan(&id, &r.System, &r.Host, &r.Kind, &r.NodeID, &r.State, &merged, &mergedAt, &r.MergeSha, &r.HeadSha, &checks, &incomplete,
		&updated, &read, &r.IssueETag, &r.PullETag, &r.Error, &errAt); err != nil {
		return "", LinkRead{}, err
	}
	r.Merged = merged != 0
	r.MergedAt, r.UpdatedAt, r.ReadAt, r.ErrorAt = unstamp(mergedAt), unstamp(updated), unstamp(read), unstamp(errAt)
	if json.Unmarshal([]byte(checks), &r.Checks) != nil || r.Checks == nil {
		r.Checks = []LinkCheck{}
	}
	if incomplete != "" {
		var in ChecksIncomplete
		if json.Unmarshal([]byte(incomplete), &in) == nil {
			r.Incomplete = &in
		}
	}
	return id, r, nil
}

// LinkReadOf reads the last read of the link with the id whole, its checks
// among it; found is false when the link has none. ErrNotFound when there
// is no such link.
func (s *Store) LinkReadOf(ctx context.Context, linkID string) (r LinkRead, found bool, err error) {
	err = s.read(ctx, func(q queryer) error {
		var n int
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM links WHERE id = ?", linkID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if schemaOf(q) < 4 {
			return nil
		}
		_, r, err = scanLinkRead(q.QueryRowContext(ctx, "SELECT "+linkReadColumns+" FROM link_reads WHERE link_id = ?", linkID))
		switch {
		case isNoRows(err):
			return nil
		case err != nil:
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return LinkRead{}, false, err
	}
	return r, found, nil
}

// linkReadsOf reads the reads of the ticket's live links, by link id; none
// for a database before version 4, read as it was found.
func linkReadsOf(ctx context.Context, q queryer, ticketID string) (map[string]LinkRead, error) {
	out := map[string]LinkRead{}
	if schemaOf(q) < 4 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT "+linkReadColumns+" FROM link_reads WHERE ticket_id = ? AND link_id IN (SELECT id FROM links WHERE ticket_id = ? AND deleted_at = '')",
		ticketID, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		id, r, err := scanLinkRead(rows)
		if err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}
