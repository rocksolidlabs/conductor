package dispatch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The list (List) is how the pages, the palette and the CLI read many
// tickets at once: a Query's filter compiled into one SQL statement from
// the code's own fragments, every value a client gave bound as a
// parameter and none written into the statement; sorted on the server and
// read a page at a time, the cursor the last row's sort key and id,
// opaque to the client; and full-text search through tickets_fts, an
// entry of each ticket's own and one of each live comment, which the store
// rewrites in the transaction of every row they index (indexTicket,
// indexComment), so a read never finds a ticket the search does not, nor
// the reverse. A read of more than one statement (a page and its total, a
// ticket with its sets) runs in one read transaction (Store.read): one
// state, whatever commits meanwhile, from this process or another.

// ErrBadQuery is a query the store will not run: a value outside its
// vocabulary or past its bound, or a cursor of another list's or of none.
// The API answers 400 invalid_request.
var ErrBadQuery = errors.New("dispatch: bad query")

// badQuery is ErrBadQuery with the words.
func badQuery(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadQuery, fmt.Sprintf(format, args...))
}

// The sorts of a list, each by one key and then the ticket's id: the
// time it was last updated or made; its priority (none, 0, first); its
// status in a board's order (Statuses); its title, without regard to the
// case of ASCII letters; its key (its project's key, then its number);
// its milestone (by the milestone's target date, those with a target
// first, then by the milestone's name and id, the tickets with no live
// milestone last). Desc reverses the whole order, the id's included.
const (
	SortUpdated   = "updated"
	SortCreated   = "created"
	SortPriority  = "priority"
	SortStatus    = "status"
	SortTitle     = "title"
	SortKey       = "key"
	SortMilestone = "milestone"
)

// Sorts is the sorts of a list; "" is SortUpdated.
var Sorts = []string{SortUpdated, SortCreated, SortPriority, SortStatus, SortTitle, SortKey, SortMilestone}

// Filter is what a list filters and sorts by. A list of values matches a
// ticket holding any of them (a status, a priority, a live label by its
// id, an assignee as person:<name> or agent:<catalog id>, a horizon, ""
// being not planned); Owner a ticket's owner by name; Milestone a live
// milestone by its id; Parent the children of a ticket by its key,
// whether it is live or not (a deleted parent's children keep their
// parent); Blocked the tickets a live, open ticket blocks; Q the words of
// a search (matchTerms); Deleted the tombstones beside the live tickets,
// which a list leaves out unless asked. Every condition given must hold.
// A value naming nothing the store holds matches nothing.
type Filter struct {
	Status    []string `json:"status,omitempty"`
	Priority  []int    `json:"priority,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Owner     string   `json:"owner,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Milestone string   `json:"milestone,omitempty"`
	Horizon   []string `json:"horizon,omitempty"`
	Parent    string   `json:"parent,omitempty"`
	Blocked   bool     `json:"blocked,omitempty"`
	Q         string   `json:"q,omitempty"`
	Deleted   bool     `json:"deleted,omitempty"`
	Sort      string   `json:"sort,omitempty"`
	Desc      bool     `json:"desc,omitempty"`
}

// Query is a list: its project by its key ("" for every project), its
// filter, and its page: at most Limit tickets (DefaultListLimit when 0,
// at most MaxListLimit), after the row Cursor names (a page's Next; ""
// for the first page).
type Query struct {
	Project string `json:"project,omitempty"`
	Filter
	// External is a URL, at most MaxLinkURL bytes: the tickets whose linked
	// issue's page, or one of whose live links' URL, it is (a pasted
	// GitHub link's search; Program item IN1a).
	External string `json:"external,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
}

// Page is a page of a list: its tickets as summaries, without their
// descriptions; Next, the cursor of the page after it, "" on the last;
// and Total, how many tickets the whole list holds.
type Page struct {
	Tickets []TicketSummary `json:"tickets"`
	Next    string          `json:"next,omitempty"`
	Total   int             `json:"total"`
}

// TicketSummary is a ticket as a list, a board or a roadmap shows it: its
// row without its description, acceptance criteria and acceptance, its
// assignees by actor, its live labels by id (by name), its milestone ""
// when it is a tombstone, and whether a live, open ticket blocks it.
type TicketSummary struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Number      int64     `json:"number"`
	Key         string    `json:"key"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Priority    int       `json:"priority"`
	Horizon     string    `json:"horizon"`
	Owner       string    `json:"owner"`
	Assignees   []string  `json:"assignees"`
	Labels      []string  `json:"labels"`
	MilestoneID string    `json:"milestoneId"`
	ParentID    string    `json:"parentId"`
	Blocked     bool      `json:"blocked"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	ClosedAt    time.Time `json:"closedAt,omitzero"`
	DeletedAt   time.Time `json:"deletedAt,omitzero"`
	Rev         int64     `json:"rev"`
}

// check checks the filter's values: each in its vocabulary and within
// its bound, ErrBadQuery otherwise.
func (f Filter) check() error {
	for _, l := range []struct {
		what string
		n    int
	}{{"statuses", len(f.Status)}, {"priorities", len(f.Priority)}, {"labels", len(f.Labels)}, {"assignees", len(f.Assignees)}, {"horizons", len(f.Horizon)}} {
		if l.n > MaxFilterValues {
			return badQuery("a filter names at most %d %s, not %d", MaxFilterValues, l.what, l.n)
		}
	}
	for _, st := range f.Status {
		if err := checkStatus(st); err != nil {
			return badQuery("%s", refusalWords(err))
		}
	}
	for _, p := range f.Priority {
		if err := checkPriority(p); err != nil {
			return badQuery("%s", refusalWords(err))
		}
	}
	for _, id := range f.Labels {
		if !ValidID(id) {
			return badQuery("the label %q is not a label's id", id)
		}
	}
	if err := checkOwner(f.Owner); err != nil {
		return badQuery("%s", refusalWords(err))
	}
	for _, a := range f.Assignees {
		if err := checkAssignee(a); err != nil {
			return badQuery("%s", refusalWords(err))
		}
	}
	if f.Milestone != "" && !ValidID(f.Milestone) {
		return badQuery("the milestone %q is not a milestone's id", f.Milestone)
	}
	for _, h := range f.Horizon {
		if err := checkHorizon(h); err != nil {
			return badQuery("%s", refusalWords(err))
		}
	}
	if f.Parent != "" {
		if _, _, ok := ParseKey(f.Parent); !ok {
			return badQuery("the parent %q is not a ticket's key", f.Parent)
		}
	}
	if !utf8.ValidString(f.Q) {
		return badQuery("the search is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(f.Q); n > MaxQueryText {
		return badQuery("the search is %d characters, more than %d", n, MaxQueryText)
	}
	if f.Sort != "" && !slices.Contains(Sorts, f.Sort) {
		return badQuery("the sort %q is none of %s", f.Sort, strings.Join(Sorts, ", "))
	}
	return nil
}

// refusalWords is the words of a refusal a field's check gave.
func refusalWords(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Words
	}
	return err.Error()
}

// matchTerms is the terms of a search's text, each the FTS5 query of its
// own condition (docs/plans/dispatch.md, Owner decisions, 14: a typed
// query is words, never FTS syntax): the text cut into terms at white
// space and control characters (a NUL inside a term would end FTS5's
// reading of it); a term with no letter or digit dropped, since the
// unicode61 tokenizer makes no token of punctuation alone and a quote or
// a star by itself would match nothing; every term kept a quoted string
// with its '"' doubled, so CND-12, OR, NOT, a:b and (x) are words to
// match and never syntax; the last one a prefix ("term"*), so a word
// being typed matches the words it begins. A term is kept once however
// often the text holds it, as the tokenizer reads it (termKey: the and THE
// alike), since each costs the reading of every entry it matches; the
// last one too when an earlier term is the same word, which, whole,
// matches only where the prefix would. A ticket matches the search when
// every term matches one of its entries, its own or a comment's, not
// necessarily the same one (termMatch). ok is false when the text holds
// terms and none is kept: the search matches nothing, as FTS5 answers a
// phrase of no tokens. Text of no term at all is no search (ok true, no
// terms).
func matchTerms(text string) (terms []string, ok bool) {
	fields := strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	if len(fields) == 0 {
		return nil, true
	}
	var kept []string
	for _, field := range fields {
		if strings.IndexFunc(field, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			kept = append(kept, field)
		}
	}
	if len(kept) == 0 {
		return nil, false
	}
	quote := func(term string) string { return `"` + strings.ReplaceAll(term, `"`, `""`) + `"` }
	seen := map[string]bool{}
	for _, term := range kept[:len(kept)-1] {
		if k := termKey(term); !seen[k] {
			seen[k] = true
			terms = append(terms, quote(term))
		}
	}
	if last := kept[len(kept)-1]; !seen[termKey(last)] {
		terms = append(terms, quote(last)+"*")
	}
	return terms, true
}

// termKey is a search term as the tokenizer reads it, as far as Go can
// tell without the tokenizer, so that two terms of one key are the same
// words to FTS5: unicode61 folds ASCII letters to lower case and makes a
// separator of every ASCII character that is neither a letter nor a
// digit, a run of them as one (Deploy, DEPLOY and (deploy) alike; CND-12
// and cnd_12 alike, and unlike cnd12). A character past ASCII is kept as
// typed: the tokenizer folds its case and diacritics by tables of its
// own, of Unicode 6.1, which Go has no copy of, and a fold that differed
// from them would make one term of two that FTS5 tells apart, a search
// then finding more than it should (Go folds Cherokee's letters, which
// the tokenizer does not). So crème and CRÈME stay two terms: a cost,
// never a wrong answer.
func termKey(term string) string {
	var b strings.Builder
	sep := false
	for _, r := range term {
		switch {
		case 'A' <= r && r <= 'Z':
			r += 'a' - 'A'
		case r < utf8.RuneSelf && !('a' <= r && r <= 'z' || '0' <= r && r <= '9'):
			sep = b.Len() > 0
			continue
		}
		if sep {
			b.WriteByte(' ')
			sep = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// execer is what a write goes through: an operation's transaction.
type execer interface {
	queryer
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// The search index holds an entry of each ticket's own, its key, title,
// description and acceptance criteria, and an entry of each of its live
// comments, the comment's body (docs/plans/dispatch.md, Owner decisions,
// 20): a comment's add, edit or delete rewrites that comment's entry
// alone, and an edit of the ticket's text its own entry alone, so a
// write's cost is its own text's, whatever else the ticket holds
// (BenchmarkIndexedWrite). A deleted ticket keeps its entries and its
// comments theirs, and a list leaves it out unless asked; a deleted
// comment has none.

// searchEntry is one entry of the search index: a ticket's own, or a
// comment's, by the row's id.
type searchEntry struct {
	comment bool
	id      string
}

// index rewrites the entry in the caller's transaction.
func (e searchEntry) index(ctx context.Context, ex execer) error {
	if e.comment {
		return indexComment(ctx, ex, e.id)
	}
	return indexTicket(ctx, ex, e.id)
}

// entryShift places a ticket in the rowid of each of its search entries
// (entryRowid), so a search reads an entry's ticket off the entry's rowid
// and reads no row to find it (termMatch).
const entryShift = 32

// entryRowid is the rowid of a search entry: its ticket's rowid shifted
// left by entryShift, plus its comment's rowid for a comment's entry (0
// for the ticket's own). A ticket's entries so lie in one range of rowids
// and no two entries share one. An error past what the rowids can hold:
// a ticket's rowid of 2^31 or more, or a comment's of 2^32 or more (some
// two billion tickets, four billion comments).
func entryRowid(ticket, comment int64) (int64, error) {
	if ticket < 1 || ticket >= 1<<(63-entryShift) || comment < 0 || comment >= 1<<entryShift {
		return 0, fmt.Errorf("dispatch: the search index holds no entry of ticket rowid %d and comment rowid %d", ticket, comment)
	}
	return ticket<<entryShift | comment, nil
}

// indexTicket rewrites the own search entry of the ticket with the id in
// the caller's transaction (Apply calls it on the ticket's create and on
// a set of its title, description or acceptance criteria, searchedBy):
// its key, title, description and acceptance criteria.
func indexTicket(ctx context.Context, ex execer, ticketID string) error {
	var rowid int64
	if err := ex.QueryRowContext(ctx, "SELECT rowid FROM tickets WHERE id = ?", ticketID).Scan(&rowid); err != nil {
		return fmt.Errorf("dispatch: the search entry of ticket %s: %w", ticketID, err)
	}
	entry, err := entryRowid(rowid, 0)
	if err != nil {
		return err
	}
	if _, err := ex.ExecContext(ctx, "DELETE FROM tickets_fts WHERE rowid = ?", entry); err != nil {
		return err
	}
	_, err = ex.ExecContext(ctx, `INSERT INTO tickets_fts (rowid, key, title, description, acceptance)
		SELECT ?, key, title, description, acceptance FROM tickets WHERE rowid = ?`, entry, rowid)
	return err
}

// indexComment rewrites the search entry of the comment with the id in
// the caller's transaction (Apply calls it on every row of the comment,
// searchedBy): its body while it is live, and none once it is deleted.
func indexComment(ctx context.Context, ex execer, commentID string) error {
	var ticket, comment int64
	var deleted string
	if err := ex.QueryRowContext(ctx, "SELECT t.rowid, c.rowid, c.deleted_at FROM comments c JOIN tickets t ON t.id = c.ticket_id WHERE c.id = ?",
		commentID).Scan(&ticket, &comment, &deleted); err != nil {
		return fmt.Errorf("dispatch: the search entry of comment %s: %w", commentID, err)
	}
	if comment < 1 {
		// Rowid 0 would be the entry of the ticket's own.
		return fmt.Errorf("dispatch: the search index holds no entry of comment rowid %d", comment)
	}
	entry, err := entryRowid(ticket, comment)
	if err != nil {
		return err
	}
	if _, err := ex.ExecContext(ctx, "DELETE FROM tickets_fts WHERE rowid = ?", entry); err != nil {
		return err
	}
	if deleted != "" {
		return nil
	}
	_, err = ex.ExecContext(ctx, "INSERT INTO tickets_fts (rowid, body) SELECT ?, body FROM comments WHERE rowid = ?", entry, comment)
	return err
}

// sortKey is one key of a sort: an expression of the code's over the
// list's tables (t the tickets, p the projects, m the live milestones),
// an integer or text, text compared without regard to ASCII case when
// nocase is set.
type sortKey struct {
	expr   string
	num    bool
	nocase bool
}

// listSort is a sort's keys and the join they need.
type listSort struct {
	join string
	keys []sortKey
}

// statusOrder is a ticket's status as its place in a board's order.
var statusOrder = func() string {
	var b strings.Builder
	b.WriteString("CASE t.status")
	for i, st := range Statuses {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", st, i)
	}
	fmt.Fprintf(&b, " ELSE %d END", len(Statuses))
	return b.String()
}()

// listSorts is every sort's keys.
var listSorts = map[string]listSort{
	SortUpdated:  {keys: []sortKey{{expr: "t.updated_at"}}},
	SortCreated:  {keys: []sortKey{{expr: "t.created_at"}}},
	SortPriority: {keys: []sortKey{{expr: "t.priority", num: true}}},
	SortStatus:   {keys: []sortKey{{expr: statusOrder, num: true}}},
	SortTitle:    {keys: []sortKey{{expr: "t.title", nocase: true}}},
	SortKey:      {join: " JOIN projects p ON p.id = t.project_id", keys: []sortKey{{expr: "p.key"}, {expr: "t.number", num: true}}},
	SortMilestone: {join: " LEFT JOIN milestones m ON m.id = t.milestone_id AND m.deleted_at = ''", keys: []sortKey{
		{expr: "CASE WHEN m.id IS NULL THEN 2 WHEN m.target = '' THEN 1 ELSE 0 END", num: true},
		{expr: "COALESCE(m.target, '')"},
		{expr: "COALESCE(m.name, '')", nocase: true},
		{expr: "COALESCE(m.id, '')"},
	}},
}

// blockedExpr says a live, open ticket blocks the ticket t, for one
// ticket; blockedSet says the same of every ticket t of a list, its
// subquery run once.
var (
	blockedExpr = fmt.Sprintf(`EXISTS (SELECT 1 FROM relations r JOIN tickets b ON b.id = r.from_id
	WHERE r.to_id = t.id AND r.kind = '%s' AND r.deleted_at = '' AND b.deleted_at = '' AND b.status NOT IN ('%s', '%s'))`,
		RelationBlocks, TicketDone, TicketCancelled)
	blockedSet = fmt.Sprintf(`t.id IN (SELECT r.to_id FROM relations r JOIN tickets b ON b.id = r.from_id
	WHERE r.kind = '%s' AND r.deleted_at = '' AND b.deleted_at = '' AND b.status NOT IN ('%s', '%s'))`,
		RelationBlocks, TicketDone, TicketCancelled)
)

// summaryColumns is a ticket's summary as scanSummary reads it, t the
// ticket.
var summaryColumns = `t.id, t.project_id, t.number, t.key, t.title, t.status, t.priority, t.horizon, t.owner,
	(SELECT json_group_array(a.actor ORDER BY a.actor) FROM ticket_assignees a WHERE a.ticket_id = t.id),
	(SELECT json_group_array(l.id ORDER BY l.name, l.id) FROM ticket_labels tl JOIN labels l ON l.id = tl.label_id
		WHERE tl.ticket_id = t.id AND l.deleted_at = ''),
	CASE WHEN EXISTS (SELECT 1 FROM milestones ms WHERE ms.id = t.milestone_id AND ms.deleted_at = '') THEN t.milestone_id ELSE '' END,
	t.parent_id, ` + blockedExpr + `, t.created_at, t.updated_at, t.closed_at, t.deleted_at, t.rev`

// scanSummary reads a ticket's summary (summaryColumns) after the
// destinations given before it.
func scanSummary(rows *sql.Rows, before ...any) (TicketSummary, error) {
	var ts TicketSummary
	var assignees, labels, created, updated, closedAt, deleted string
	dest := append(before, &ts.ID, &ts.ProjectID, &ts.Number, &ts.Key, &ts.Title, &ts.Status, &ts.Priority, &ts.Horizon, &ts.Owner,
		&assignees, &labels, &ts.MilestoneID, &ts.ParentID, &ts.Blocked, &created, &updated, &closedAt, &deleted, &ts.Rev)
	if err := rows.Scan(dest...); err != nil {
		return TicketSummary{}, err
	}
	if err := json.Unmarshal([]byte(assignees), &ts.Assignees); err != nil {
		return TicketSummary{}, fmt.Errorf("dispatch: the assignees of %s: %w", ts.Key, err)
	}
	if err := json.Unmarshal([]byte(labels), &ts.Labels); err != nil {
		return TicketSummary{}, fmt.Errorf("dispatch: the labels of %s: %w", ts.Key, err)
	}
	ts.CreatedAt, ts.UpdatedAt, ts.ClosedAt, ts.DeletedAt = unstamp(created), unstamp(updated), unstamp(closedAt), unstamp(deleted)
	return ts, nil
}

// listPlan is a list compiled: its conditions and their arguments, its
// sort, its page.
type listPlan struct {
	conds []string
	args  []any
	sort  listSort
	name  string // the sort's name, which a cursor carries
	desc  bool
	limit int
	after *cursor
}

// plan checks the query and compiles it; none is true when it searches
// for terms that are all dropped (matchTerms), a list of nothing.
func (q Query) plan() (p listPlan, none bool, err error) {
	if q.Project != "" && !ValidKey(q.Project) {
		return listPlan{}, false, badQuery("the project %q is not a project's key", q.Project)
	}
	if err := q.Filter.check(); err != nil {
		return listPlan{}, false, err
	}
	if len(q.External) > MaxLinkURL || !utf8.ValidString(q.External) {
		return listPlan{}, false, badQuery("the URL searched for is not at most %d bytes of UTF-8", MaxLinkURL)
	}
	p.limit = q.Limit
	if p.limit == 0 {
		p.limit = DefaultListLimit
	}
	if p.limit < 1 || p.limit > MaxListLimit {
		return listPlan{}, false, badQuery("a page is 1 to %d tickets, not %d", MaxListLimit, q.Limit)
	}
	p.name = q.Sort
	if p.name == "" {
		p.name = SortUpdated
	}
	p.sort, p.desc = listSorts[p.name], q.Desc
	if q.Cursor != "" {
		c, err := decodeCursor(q.Cursor)
		if err != nil {
			return listPlan{}, false, err
		}
		if err := c.fits(p.name, p.desc, p.sort.keys); err != nil {
			return listPlan{}, false, err
		}
		p.after = &c
	}
	terms, ok := matchTerms(q.Q)
	if !ok {
		return listPlan{}, true, nil
	}
	cond := func(c string, args ...any) {
		p.conds = append(p.conds, c)
		p.args = append(p.args, args...)
	}
	in := func(column string, n int) string {
		return column + " IN (" + strings.TrimSuffix(strings.Repeat("?, ", n), ", ") + ")"
	}
	switch {
	case q.Deleted:
	case q.Project != "":
		cond("t.deleted_at = ''")
	default:
		// A list of every project keeps its tombstones out without the
		// index of deleted_at (SQLite's unary plus keeps a term from
		// choosing an index). That index's one value names almost every
		// ticket, and through it the planner read every live ticket's row,
		// wide with its description. Without it, the planner reads by
		// another term (a parent's index or an owner's, the rowids a search
		// matched), which names fewer rows, or reads tickets_list's
		// entries, which hold every column a list filters or sorts by but
		// the title (BenchmarkList).
		cond("+t.deleted_at = ''")
	}
	if q.Project != "" {
		cond("t.project_id = (SELECT id FROM projects WHERE key = ?)", q.Project)
	}
	if len(q.Status) > 0 {
		cond(in("t.status", len(q.Status)), anys(q.Status)...)
	}
	if len(q.Priority) > 0 {
		cond(in("t.priority", len(q.Priority)), anys(q.Priority)...)
	}
	// The set filters are each one subquery, run once for the statement
	// and probed for each ticket, rather than a subquery run for each.
	if len(q.Labels) > 0 {
		cond("t.id IN (SELECT tl.ticket_id FROM ticket_labels tl JOIN labels l ON l.id = tl.label_id WHERE l.deleted_at = '' AND "+
			in("tl.label_id", len(q.Labels))+")", anys(q.Labels)...)
	}
	if q.Owner != "" {
		cond("t.owner = ?", q.Owner)
	}
	if len(q.Assignees) > 0 {
		cond("t.id IN (SELECT a.ticket_id FROM ticket_assignees a WHERE "+in("a.actor", len(q.Assignees))+")", anys(q.Assignees)...)
	}
	if q.Milestone != "" {
		cond("t.milestone_id = ? AND EXISTS (SELECT 1 FROM milestones ms WHERE ms.id = t.milestone_id AND ms.deleted_at = '')", q.Milestone)
	}
	if len(q.Horizon) > 0 {
		cond(in("t.horizon", len(q.Horizon)), anys(q.Horizon)...)
	}
	if q.Parent != "" {
		cond("t.parent_id = (SELECT id FROM tickets WHERE key = ?)", q.Parent)
	}
	if q.Blocked {
		cond(blockedSet)
	}
	if q.External != "" {
		cond("(t.external_url = ? OR t.id IN (SELECT l.ticket_id FROM links l WHERE l.url = ? AND l.deleted_at = ''))", q.External, q.External)
	}
	for _, term := range terms {
		cond(termMatch, term)
	}
	return p, false, nil
}

// termMatch is the tickets one term of a search matches (matchTerms): the
// ticket of every entry the term matches, its own or a comment's, read
// off the entry's rowid (entryRowid). Each term is a condition of its
// own, so a ticket matches when every term matches one of its entries,
// not necessarily the same one: the terms' tickets intersected
// (docs/plans/dispatch.md, Owner decisions, 20). The unary plus keeps the
// planner from reading a list through a term's set, which for a common
// word holds nearly every ticket, each then read from its wide row: the
// list reads its tickets by its other terms (its project, an owner, a
// parent) or from tickets_list's entries, and looks each up in the sets
// (BenchmarkList).
var termMatch = fmt.Sprintf("+t.rowid IN (SELECT rowid >> %d FROM tickets_fts WHERE tickets_fts MATCH ?)", entryShift)

// anys is a list of values as statement arguments.
func anys[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// where is the plan's conditions as a WHERE clause, extra among them.
func where(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// pageSQL is the one statement of the plan's page and its arguments: the
// ids and sort keys of every ticket the filter names, read once (m,
// materialized, from the list's index where the filter allows: a ticket's
// row is wide with its description); their count, the list's total, on
// every row; and the summaries of at most limit+1 of them after the
// cursor, in the sort's order (page: a subquery, so the summaries' own
// subqueries run for its rows alone). A page past the cursor that is
// empty has no row to carry the total (countSQL).
func (p listPlan) pageSQL() (string, []any) {
	dir, cmp := "", ">"
	if p.desc {
		dir, cmp = " DESC", "<"
	}
	var picks, order, outer, tuple []string
	for i, k := range p.sort.keys {
		picks = append(picks, fmt.Sprintf("%s AS k%d", k.expr, i))
		collate := ""
		if k.nocase {
			collate = " COLLATE NOCASE"
		}
		order = append(order, fmt.Sprintf("k%d%s%s", i, collate, dir))
		outer = append(outer, fmt.Sprintf("page.k%d%s%s", i, collate, dir))
		tuple = append(tuple, fmt.Sprintf("k%d%s", i, collate))
	}
	args := slices.Clone(p.args)
	after := ""
	if p.after != nil {
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(tuple)+1), ", ")
		after = " WHERE (" + strings.Join(tuple, ", ") + ", id) " + cmp + " (" + marks + ")"
		args = append(append(args, p.after.Keys...), p.after.ID)
	}
	args = append(args, p.limit+1)
	var b strings.Builder
	b.WriteString("WITH m AS MATERIALIZED (SELECT t.id AS id, " + strings.Join(picks, ", ") + " FROM tickets t" + p.sort.join + where(p.conds) + "), ")
	b.WriteString("page AS (SELECT * FROM m" + after + " ORDER BY " + strings.Join(order, ", ") + ", id" + dir + " LIMIT ?) ")
	b.WriteString("SELECT (SELECT COUNT(*) FROM m), ")
	for i := range p.sort.keys {
		fmt.Fprintf(&b, "page.k%d, ", i)
	}
	b.WriteString(summaryColumns)
	b.WriteString(" FROM page JOIN tickets t ON t.id = page.id ORDER BY " + strings.Join(outer, ", ") + ", page.id" + dir)
	return b.String(), args
}

// countSQL is the statement of the plan's total and its arguments, for a
// page past a cursor that holds no ticket.
func (p listPlan) countSQL() (string, []any) {
	return "SELECT COUNT(*) FROM tickets t" + where(p.conds), p.args
}

// List reads a page of the tickets the query names, sorted on the server,
// and how many it names in all: one statement, compiled from the filter
// (plan), in a read transaction with the count a page past its cursor
// that holds no ticket needs. A search whose terms are all dropped
// (matchTerms) is an empty page and no error. ErrBadQuery for a query the
// store will not run.
func (s *Store) List(ctx context.Context, q Query) (Page, error) {
	p, none, err := q.plan()
	if err != nil {
		return Page{}, err
	}
	page := Page{Tickets: []TicketSummary{}}
	if none {
		return page, nil
	}
	err = s.read(ctx, func(rq queryer) error {
		stmt, args := p.pageSQL()
		rows, err := rq.QueryContext(ctx, stmt, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var keys []any
		for rows.Next() {
			vals := make([]any, len(p.sort.keys))
			dest := make([]any, 1+len(p.sort.keys))
			dest[0] = &page.Total
			for i, k := range p.sort.keys {
				if k.num {
					dest[1+i] = new(int64)
				} else {
					dest[1+i] = new(string)
				}
			}
			ts, err := scanSummary(rows, dest...)
			if err != nil {
				return err
			}
			if len(page.Tickets) == p.limit {
				// One past the page: there is a next one, after the last row
				// of this one.
				page.Next = encodeCursor(cursor{Sort: p.name, Desc: p.desc, Keys: keys, ID: page.Tickets[len(page.Tickets)-1].ID})
				break
			}
			for i, d := range dest[1:] {
				switch v := d.(type) {
				case *int64:
					vals[i] = *v
				case *string:
					vals[i] = *v
				}
			}
			keys = vals
			page.Tickets = append(page.Tickets, ts)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Tickets) > 0 || p.after == nil {
			// A first page with no ticket is a list of none.
			return nil
		}
		rows.Close()
		stmt, args = p.countSQL()
		return rq.QueryRowContext(ctx, stmt, args...).Scan(&page.Total)
	})
	if err != nil {
		return Page{}, err
	}
	return page, nil
}

// cursor is where a page ends: the sort it is a page of and its
// direction, the last row's sort keys and its id. As a client holds it,
// it is opaque: base64url of its JSON.
type cursor struct {
	Sort string `json:"s"`
	Desc bool   `json:"d,omitempty"`
	Keys []any  `json:"k"`
	ID   string `json:"i"`
}

// maxCursor bounds a cursor as a client sends it, in bytes: the longest a
// list or the projects make (a title of MaxTicketTitle characters, each
// escaped at worst) is far under it.
const maxCursor = 4 << 10

// encodeCursor is the cursor as a client holds it.
func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor reads a cursor a client sent back: ErrBadQuery for one
// that is not a cursor of the store's.
func decodeCursor(s string) (cursor, error) {
	if len(s) > maxCursor {
		return cursor{}, badQuery("the cursor is more than %d bytes", maxCursor)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, badQuery("the cursor is not one of the store's")
	}
	var c cursor
	if err := decodeStrict(raw, &c, true); err != nil {
		return cursor{}, badQuery("the cursor is not one of the store's")
	}
	return c, nil
}

// decodeStrict decodes data into v: one JSON value of v's fields and no
// other, nothing after it; numbers kept as json.Number when useNumber is
// set.
func decodeStrict(data []byte, v any, useNumber bool) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if useNumber {
		dec.UseNumber()
	}
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF {
		return errors.New("more than one JSON value")
	}
	return nil
}

// fits checks that the cursor is a page's end of the list sorted by
// sort in the direction desc, whose keys are keys, and makes its keys
// the values the statement compares: ErrBadQuery otherwise.
func (c *cursor) fits(sort string, desc bool, keys []sortKey) error {
	if c.Sort != sort || c.Desc != desc {
		return badQuery("the cursor is a page of another sort")
	}
	if len(c.Keys) != len(keys) || !ValidID(c.ID) {
		return badQuery("the cursor is not one of the store's")
	}
	for i, k := range keys {
		switch v := c.Keys[i].(type) {
		case json.Number:
			n, err := strconv.ParseInt(string(v), 10, 64)
			if !k.num || err != nil {
				return badQuery("the cursor is not one of the store's")
			}
			c.Keys[i] = n
		case string:
			if k.num {
				return badQuery("the cursor is not one of the store's")
			}
		default:
			return badQuery("the cursor is not one of the store's")
		}
	}
	return nil
}

// Children reads a page of the live children of the ticket with the key,
// as List reads a list whose Parent is it, by number (SortKey): limit is
// a list's, and next the Next of the page before ("" for the first). A
// deleted ticket's children keep their parent and are found so, though
// its own read carries none (Ticket).
func (s *Store) Children(ctx context.Context, key string, limit int, next string) (Page, error) {
	return s.List(ctx, Query{Filter: Filter{Parent: key, Sort: SortKey}, Limit: limit, Cursor: next})
}

// Blockers reads what blocks the ticket with the key now, by number: the
// live tickets, neither done nor cancelled, of whose blocks relations a
// live one names it, each once; the ticket is blocked exactly while there
// is one. A deleted blocker blocks nothing while it is deleted, and
// blocks again on its undelete. ErrNotFound when there is no such ticket.
func (s *Store) Blockers(ctx context.Context, key string) ([]RelatedTicket, error) {
	var out []RelatedTicket
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT b.number, b.id, b.key, b.title, b.status FROM relations r JOIN tickets b ON b.id = r.from_id
			WHERE r.to_id = ? AND r.kind = ? AND r.deleted_at = '' AND b.deleted_at = '' AND b.status NOT IN (?, ?) ORDER BY b.number`,
			tk.ID, RelationBlocks, TicketDone, TicketCancelled)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []RelatedTicket{}
		for rows.Next() {
			var number int64
			var b RelatedTicket
			if err := rows.Scan(&number, &b.ID, &b.Key, &b.Title, &b.Status); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
