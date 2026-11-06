package history

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrKind is an entry of a kind the table does not hold (kinds.go).
	ErrKind = errors.New("history: unknown kind")
	// ErrClass is an entry whose caller gave its class: the class comes
	// from the kind alone.
	ErrClass = errors.New("history: a class is the index's to set")
	// ErrBound is an entry, a link or a source past a bound, missing a
	// field it needs, or of text that is not UTF-8.
	ErrBound = errors.New("history: out of bounds")
	// ErrKeyTaken is an entry whose key another source's entry holds.
	ErrKeyTaken = errors.New("history: the key is another source's entry's")
	// ErrStale is a write of a source read in a generation the index has
	// left (a Clear, a Check's clear or its file made again since): its
	// mark would stand over what the clear took. Nothing is written; the
	// source is read again.
	ErrStale = errors.New("history: the index was cleared since the source was read")
)

// Source is what entries are read from: its key ("run:<id>",
// "session:<id>", "ticket:<KEY-n>", ...) and its mark, what the source
// said when it was read (a revision, a seq), which the indexer compares
// to know whether to read it again. Generation, when it is not 0, is the
// index's generation (Generation) the source was read in: a Replace in
// any other is ErrStale.
type Source struct {
	Key        string
	Mark       string
	Generation int64
}

// SourceMark is an indexed source as the index holds it.
type SourceMark struct {
	Key       string    `json:"key"`
	Mark      string    `json:"mark"`
	IndexedAt time.Time `json:"indexedAt"`
	Bytes     int64     `json:"bytes"`
}

// Entry is one row of history. Key is its stable handle, made by its
// source from what it is (its own ref when it has one, else its anchor's
// ref and its place), the same on every read of the source and after
// every rebuild, never the row's id. Class, Digest and Source are the
// index's: a caller leaves them empty (a class given is ErrClass). Title,
// Summary and Body are indexed with U+E000 and U+E001, the snippet's
// marks, written as U+FFFD (unmark).
type Entry struct {
	Key          string    `json:"key"`
	Kind         string    `json:"kind"`
	Class        string    `json:"class"`
	Ref          string    `json:"ref"`
	Seq          int64     `json:"seq,omitempty"`
	Anchor       string    `json:"anchor"`
	At           time.Time `json:"at"`
	Host         string    `json:"host,omitempty"`
	HostUser     string    `json:"hostUser,omitempty"`
	Hosted       bool      `json:"hosted,omitempty"`
	Where        string    `json:"where,omitempty"`
	Project      string    `json:"project,omitempty"`
	Repo         string    `json:"repo,omitempty"` // the repository's identity, its common git directory
	Tree         string    `json:"tree,omitempty"` // the worktree's top, shown
	Revision     string    `json:"revision,omitempty"`
	Commit       string    `json:"commit,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	AgentVersion string    `json:"agentVersion,omitempty"`
	AgentSession string    `json:"agentSession,omitempty"`
	Crew         string    `json:"crew,omitempty"`
	Run          string    `json:"run,omitempty"`
	Ticket       string    `json:"ticket,omitempty"`
	Outcome      string    `json:"outcome,omitempty"`
	Actor        string    `json:"actor,omitempty"`
	Subject      string    `json:"subject,omitempty"` // a receipt's command: the SHA-256 of its argv
	Title        string    `json:"title"`
	Summary      string    `json:"summary,omitempty"`
	Body         string    `json:"body,omitempty"`
	Origin       string    `json:"origin,omitempty"` // reserved for team history (TS1): "" for this server's own
	Digest       string    `json:"digest,omitempty"`
	Source       string    `json:"source,omitempty"`
}

// Link is a relation between two evidence references (handed,
// passed_after, session_of, cites, ...), owned by the source that writes
// it: replaced and removed with it.
type Link struct {
	From string `json:"from"`
	Rel  string `json:"rel"`
	To   string `json:"to"`
}

// relPattern is a link's relation.
var relPattern = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// stampLayout is how a time is kept: RFC 3339 in UTC with nine digits of
// the second always written, so the text sorts as the time does.
const stampLayout = "2006-01-02T15:04:05.000000000Z"

func stamp(t time.Time) string { return t.UTC().Format(stampLayout) }

func unstamp(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// bound is ErrBound with the words.
func bound(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBound, fmt.Sprintf(format, args...))
}

// checkRef checks a key, a ref, an anchor or a link's end: given, at most
// MaxRef bytes of UTF-8 with no control character.
func checkRef(what, s string) error {
	switch {
	case s == "":
		return bound("%s is empty", what)
	case len(s) > MaxRef:
		return bound("%s is %d bytes, more than %d", what, len(s), MaxRef)
	case !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0:
		return bound("%s %q is not text", what, s)
	}
	return nil
}

// checkText checks a text against its bound.
func checkText(what, s string, n int) error {
	if len(s) > n {
		return bound("%s is %d bytes, more than %d", what, len(s), n)
	}
	if !utf8.ValidString(s) {
		return bound("%s is not valid UTF-8", what)
	}
	return nil
}

// check checks an entry and sets what the index sets: its class from its
// kind, its digest, and its texts unmarked (unmark).
func (e *Entry) check() error {
	if e.Class != "" {
		return fmt.Errorf("%w: the entry %s gives the class %q", ErrClass, e.Key, e.Class)
	}
	class, ok := classOf(e.Kind)
	if !ok {
		return fmt.Errorf("%w: %q (the entry %s)", ErrKind, e.Kind, e.Key)
	}
	for _, r := range []struct{ what, s string }{{"the key", e.Key}, {"the ref", e.Ref}, {"the anchor", e.Anchor}} {
		if err := checkRef(r.what, r.s); err != nil {
			return err
		}
	}
	if e.At.IsZero() {
		return bound("the entry %s has no time", e.Key)
	}
	if e.Seq < 0 {
		return bound("the entry %s has a negative seq", e.Key)
	}
	if e.Title == "" {
		return bound("the entry %s has no title", e.Key)
	}
	for _, f := range []struct {
		what string
		s    string
		n    int
	}{
		{"the title", e.Title, MaxTitle}, {"the summary", e.Summary, MaxSummary}, {"the body", e.Body, bodyBound(e.Kind)},
		{"the host", e.Host, MaxField}, {"the host's user", e.HostUser, MaxField}, {"where", e.Where, MaxField},
		{"the project", e.Project, MaxField}, {"the repository", e.Repo, MaxField}, {"the tree", e.Tree, MaxField},
		{"the revision", e.Revision, MaxField}, {"the commit", e.Commit, MaxField}, {"the agent", e.Agent, MaxField},
		{"the agent's version", e.AgentVersion, MaxField}, {"the agent's session", e.AgentSession, MaxField},
		{"the crew", e.Crew, MaxField}, {"the run", e.Run, MaxField}, {"the ticket", e.Ticket, MaxField},
		{"the outcome", e.Outcome, MaxField}, {"the actor", e.Actor, MaxField}, {"the subject", e.Subject, MaxField},
		{"the origin", e.Origin, MaxField},
	} {
		if err := checkText(f.what+" of "+e.Key, f.s, f.n); err != nil {
			return err
		}
	}
	if slices.Contains(needsCommit, e.Kind) && e.Commit == "" {
		return bound("the %s entry %s names no commit: a receipt is bound to the commit it checked", e.Kind, e.Key)
	}
	if class == ClassDecided && e.Actor == "" {
		return bound("the %s entry %s names no actor: a decision is a person's", e.Kind, e.Key)
	}
	e.Title, e.Summary, e.Body = unmark.Replace(e.Title), unmark.Replace(e.Summary), unmark.Replace(e.Body)
	e.Class, e.Digest = class, digest(e.Title, e.Summary, e.Body)
	return nil
}

// unmark writes U+E000 and U+E001, where snippet() marks a match
// (snipOpen, snipClose), as U+FFFD, of the same three bytes: a text that
// held them would otherwise give a snippet marks the search never made.
var unmark = strings.NewReplacer(snipOpen, replacement, snipClose, replacement)

// digest is the SHA-256 of an entry's body folded (its case folded, its
// white space one space), so the same words said in several places are
// grouped as repeats and never merged; an entry with no body is digested
// by its title and summary.
func digest(title, summary, body string) string {
	text := fold(body)
	if text == "" {
		text = "\x00" + fold(title) + "\x00" + fold(summary)
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// entryBytes is the text an entry holds, which the budget counts.
func entryBytes(e Entry) int64 { return int64(len(e.Title) + len(e.Summary) + len(e.Body)) }

// Replace replaces the source's entries and links with those given, in
// one transaction, all or nothing: the old rows and their search rows
// deleted, the new inserted, the source's mark and bytes written, and the
// budget kept (budget). Every entry is checked first: its kind in the
// table (ErrKind), no class of the caller's (ErrClass), its fields within
// their bounds (ErrBound); a key another source's entry holds is
// ErrKeyTaken. A link another source wrote is this one's after it. A
// source read in another generation than the index's is ErrStale, judged
// in the write's own transaction, so no clear comes between the judgement
// and the commit.
func (ix *Index) Replace(ctx context.Context, src Source, entries []Entry, links []Link) error {
	if err := checkRef("the source's key", src.Key); err != nil {
		return err
	}
	if err := checkText("the source's mark", src.Mark, MaxRef); err != nil {
		return err
	}
	rows := make([]Entry, len(entries))
	keys := make(map[string]bool, len(entries))
	var total int64
	for i, e := range entries {
		if err := e.check(); err != nil {
			return err
		}
		if keys[e.Key] {
			return bound("the key %s is given twice", e.Key)
		}
		keys[e.Key] = true
		rows[i] = e
		total += entryBytes(e)
	}
	seen := make(map[Link]bool, len(links))
	var kept []Link
	for _, l := range links {
		for _, end := range []struct{ what, s string }{{"a link's from", l.From}, {"a link's to", l.To}} {
			if err := checkRef(end.what, end.s); err != nil {
				return err
			}
		}
		if !relPattern.MatchString(l.Rel) {
			return bound("the link relation %q is not one", l.Rel)
		}
		if !seen[l] {
			seen[l] = true
			kept = append(kept, l)
		}
	}
	if err := ix.enter(); err != nil {
		return err
	}
	defer ix.leave()
	tx, end, err := ix.writeTx(ctx)
	if err != nil {
		return err
	}
	defer end()
	if src.Generation != 0 {
		_, gen, err := readVersion(ctx, tx)
		if err != nil {
			return err
		}
		if gen != src.Generation {
			return ErrStale
		}
	}
	if err := unindexSource(ctx, tx, src.Key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM entries WHERE source = ?", src.Key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM links WHERE source = ?", src.Key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sources (key, mark, indexed_at, bytes) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET mark = excluded.mark, indexed_at = excluded.indexed_at, bytes = excluded.bytes`,
		src.Key, src.Mark, stamp(ix.now()), total); err != nil {
		return err
	}
	if err := insertEntries(ctx, tx, src.Key, rows); err != nil {
		return err
	}
	if len(kept) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO links (from_ref, rel, to_ref, source) VALUES (?, ?, ?, ?)
			ON CONFLICT (from_ref, rel, to_ref) DO UPDATE SET source = excluded.source`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, l := range kept {
			if _, err := stmt.ExecContext(ctx, l.From, l.Rel, l.To, src.Key); err != nil {
				return err
			}
		}
	}
	if err := ix.budget(ctx, tx); err != nil {
		return err
	}
	if ix.beforeCommit != nil {
		if err := ix.beforeCommit(); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertEntries inserts the source's entries and their search rows.
func insertEntries(ctx context.Context, tx *sql.Tx, source string, rows []Entry) error {
	if len(rows) == 0 {
		return nil
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO entries (key, source, kind, class, ref, seq, anchor, at,
		host, host_user, hosted, where_ran, project, repo, tree, revision, commit_id, agent, agent_version, agent_session,
		crew, run, ticket, outcome, actor, subject_key, digest, bytes, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	texts, err := tx.PrepareContext(ctx, "INSERT INTO texts (id, title, summary, body) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer texts.Close()
	fts, err := tx.PrepareContext(ctx, "INSERT INTO entries_fts (rowid, title, summary, body) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer fts.Close()
	for _, e := range rows {
		res, err := ins.ExecContext(ctx, e.Key, source, e.Kind, e.Class, e.Ref, e.Seq, e.Anchor, stamp(e.At),
			e.Host, e.HostUser, e.Hosted, e.Where, e.Project, e.Repo, e.Tree, e.Revision, e.Commit, e.Agent, e.AgentVersion, e.AgentSession,
			e.Crew, e.Run, e.Ticket, e.Outcome, e.Actor, e.Subject, e.Digest, entryBytes(e), e.Origin)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: %s", ErrKeyTaken, e.Key)
			}
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := texts.ExecContext(ctx, id, e.Title, e.Summary, e.Body); err != nil {
			return err
		}
		if _, err := fts.ExecContext(ctx, id, e.Title, e.Summary, e.Body); err != nil {
			return err
		}
	}
	return nil
}

// unindexSource deletes the search rows of the source's entries, which an
// external-content table is told with the rows' own text, before the rows
// go (their texts with them, by cascade).
func unindexSource(ctx context.Context, tx *sql.Tx, source string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO entries_fts (entries_fts, rowid, title, summary, body)
		SELECT 'delete', t.id, t.title, t.summary, t.body FROM entries e JOIN texts t ON t.id = e.id WHERE e.source = ?`, source)
	return err
}

// Remove deletes a source and, by cascade, its entries and links, their
// search rows with them. A source the index does not hold is no error.
func (ix *Index) Remove(ctx context.Context, key string) error {
	if err := ix.enter(); err != nil {
		return err
	}
	defer ix.leave()
	tx, end, err := ix.writeTx(ctx)
	if err != nil {
		return err
	}
	defer end()
	if err := unindexSource(ctx, tx, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sources WHERE key = ?", key); err != nil {
		return err
	}
	return tx.Commit()
}

// Generation is the index's generation, which every clear moves: what a
// source read now gives its Replace (Source.Generation).
func (ix *Index) Generation(ctx context.Context) (int64, error) {
	var out int64
	err := ix.read(ctx, false, func(_ *sql.Tx, gen int64) error {
		out = gen
		return nil
	})
	return out, err
}

// Mark reads a source's mark; ok is false for a source the index does
// not hold.
func (ix *Index) Mark(ctx context.Context, key string) (mark string, ok bool, err error) {
	err = ix.read(ctx, false, func(tx *sql.Tx, _ int64) error {
		err := tx.QueryRowContext(ctx, "SELECT mark FROM sources WHERE key = ?", key).Scan(&mark)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		ok = err == nil
		return err
	})
	return mark, ok, err
}

// MaxSourcesPage is the most sources one call of Sources answers.
const MaxSourcesPage = 1000

// Sources is a page of the indexed sources in the order of their keys,
// after the key given ("" for the first), at most limit (1 to
// MaxSourcesPage): what a catch-up compares with the stores.
func (ix *Index) Sources(ctx context.Context, after string, limit int) ([]SourceMark, error) {
	if limit < 1 || limit > MaxSourcesPage {
		limit = MaxSourcesPage
	}
	var out []SourceMark
	err := ix.read(ctx, false, func(tx *sql.Tx, _ int64) error {
		rows, err := tx.QueryContext(ctx, "SELECT key, mark, indexed_at, bytes FROM sources WHERE key > ? ORDER BY key LIMIT ?", after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s SourceMark
			var at string
			if err := rows.Scan(&s.Key, &s.Mark, &at, &s.Bytes); err != nil {
				return err
			}
			s.IndexedAt = unstamp(at)
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// budget keeps the index within MaxEntries entries and MaxBytes of text
// in the caller's transaction: past either, the oldest entries by when
// they happened go first (then by key, so the cut is one), by age alone
// and never by class. A source all of whose entries are older than the
// cut goes whole, its links with it, its row kept with its mark, so the
// indexer does not read it again for nothing; of another, only the
// entries past the cut. How many went is added to what Stats says.
func (ix *Index) budget(ctx context.Context, tx *sql.Tx) error {
	var n int
	var bytes int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM entries").Scan(&n); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(bytes), 0) FROM sources").Scan(&bytes); err != nil {
		return err
	}
	if n <= ix.maxEntries && bytes <= ix.maxBytes {
		return nil
	}
	needN, needB := n-ix.maxEntries, bytes-ix.maxBytes
	type cut struct {
		ids   []int64
		bytes int64
	}
	bySource := map[string]*cut{}
	var order []string
	rows, err := tx.QueryContext(ctx, "SELECT id, source, bytes FROM entries ORDER BY at, key")
	if err != nil {
		return err
	}
	var dropN int
	var dropB int64
	for (dropN < needN || dropB < needB) && rows.Next() {
		var id, b int64
		var source string
		if err := rows.Scan(&id, &source, &b); err != nil {
			rows.Close()
			return err
		}
		c := bySource[source]
		if c == nil {
			c = &cut{}
			bySource[source] = c
			order = append(order, source)
		}
		c.ids = append(c.ids, id)
		c.bytes += b
		dropN++
		dropB += b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, source := range order {
		c := bySource[source]
		var held int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM entries WHERE source = ?", source).Scan(&held); err != nil {
			return err
		}
		if held == len(c.ids) {
			if err := unindexSource(ctx, tx, source); err != nil {
				return err
			}
			for _, stmt := range []string{"DELETE FROM entries WHERE source = ?", "DELETE FROM links WHERE source = ?", "UPDATE sources SET bytes = 0 WHERE key = ?"} {
				if _, err := tx.ExecContext(ctx, stmt, source); err != nil {
					return err
				}
			}
			continue
		}
		for _, id := range c.ids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO entries_fts (entries_fts, rowid, title, summary, body)
				SELECT 'delete', id, title, summary, body FROM texts WHERE id = ?`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM entries WHERE id = ?", id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sources SET bytes = max(bytes - ?, 0) WHERE key = ?", c.bytes, source); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE schema_version SET dropped = dropped + ?", dropN)
	return err
}

// Stats is what the index holds: its generation, its entries, sources and
// bytes of text, the oldest time it still holds (history before it was
// dropped by the budget, or never indexed) and how many entries the
// budget let go since the index was last cleared.
type Stats struct {
	State      string    `json:"state,omitempty"` // StateOtherVersion, StateMissing (no numbers); StateChecking (with them); "" for this version's, checked
	Generation int64     `json:"generation,omitempty"`
	Entries    int       `json:"entries"`
	Sources    int       `json:"sources"`
	Bytes      int64     `json:"bytes"`
	Oldest     time.Time `json:"oldest,omitzero"`
	Dropped    int64     `json:"dropped,omitempty"`
}

// Stats reads the index's Stats; Entries and Oldest leave out the entries
// of the sources except, as a search's ExceptSources does.
func (ix *Index) Stats(ctx context.Context, except ...string) (Stats, error) {
	var st Stats
	if err := ix.enterRead(ctx); err != nil {
		return st, err
	}
	defer ix.leave()
	tx, end, gen, state, err := ix.readTx(ctx)
	defer end()
	st.State = state
	if err != nil || tx == nil {
		return st, err
	}
	st.Generation = gen
	var oldest sql.NullString
	count, args := "SELECT COUNT(*), MIN(at) FROM entries", []any(nil)
	if c, a := exceptSources("source", except); c != "" {
		count, args = count+" WHERE "+c, a
	}
	if err := tx.QueryRowContext(ctx, count, args...).Scan(&st.Entries, &oldest); err != nil {
		return st, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM sources").Scan(&st.Sources, &st.Bytes); err != nil {
		return st, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT dropped FROM schema_version").Scan(&st.Dropped); err != nil {
		return st, err
	}
	if oldest.Valid {
		st.Oldest = unstamp(oldest.String)
	}
	return st, nil
}
