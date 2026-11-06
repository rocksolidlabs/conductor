package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The bounds of a search (docs/plans/history.md, "The search").
const (
	MaxQuery        = 512 // bytes of the line
	MaxTerms        = 16  // words, phrases and filters
	MaxFilterValues = 16  // a filter's alternatives
	DefaultLimit    = 20
	MaxLimit        = 100
	MaxTotal        = 10_000 // the count stops here (Page.TotalCapped)
	MaxSnippet      = 300
	MaxLinks        = 100 // each way, Links
	pageLinks       = 8   // each way, on a page's row
	MaxSimilar      = 50
	// maxCursor is a cursor's bound: a key of MaxRef bytes, each escaped
	// at its worst in JSON, base64'd, fits under it.
	maxCursor = 2 << 10

	// MaxScanned and MaxRanked bound the work of a search of words, so
	// that a word in every entry costs what a rare one does (Page.Cut): it
	// reads at most MaxScanned of the entries its words match, the most
	// recently indexed first (FTS5 walks its doclist from the newest rowid
	// and stops), and scores (bm25) at most MaxRanked of those that pass
	// its filters.
	MaxScanned = 10_000
	MaxRanked  = 2_000
)

// The bounds a search of words can stop at (Page.Cut). Neither makes an
// answer wrong: each says what was read.
const (
	// CutRanked: more than MaxRanked of the entries the search read pass
	// its filters; the MaxRanked most recently indexed of them are ranked
	// and paged, and Total counts every one that passes.
	CutRanked = "ranked"
	// CutScanned: the words match more than MaxScanned entries; the
	// MaxScanned most recently indexed are read, those of them that pass
	// the filters are answered newest first, unscored, and Total counts
	// every match when there are no filters, else those it read that pass
	// them.
	CutScanned = "scanned"
)

var (
	// ErrQuery is a line the search will not run, with the words why.
	ErrQuery = errors.New("history: bad query")
	// ErrCursor is a cursor that does not decode, or of another query.
	ErrCursor = errors.New("history: bad cursor")
	// ErrStaleCursor is a cursor taken before the index was cleared (a
	// reindex, a rebuild of another version): the history was rebuilt;
	// search again.
	ErrStaleCursor = errors.New("history: the history was rebuilt; search again")
	// ErrGone is a key the index no longer holds.
	ErrGone = errors.New("history: this entry is no longer in the history")
)

func badQuery(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrQuery, fmt.Sprintf(format, args...))
}

// Query is a search, parsed from a person's line by ParseQuery; Limit (0:
// DefaultLimit, at most MaxLimit) and After (a page's Next) are its page.
type Query struct {
	Limit int
	After string

	terms   []string // FTS5 strings, each a condition of its own
	none    bool     // words were typed and none is kept: nothing matches
	filters []filter
	since   *when
	until   *when
}

// filter is one name:value of a line: a condition any of whose values
// matches.
type filter struct {
	Name   string   `json:"n"`
	Values []string `json:"v"`
}

// when is a since or until: a time, or a span back from now.
type when struct {
	At   time.Time     `json:"a,omitzero"`
	Back time.Duration `json:"b,omitempty"`
	Date bool          `json:"d,omitempty"` // a day: until takes the whole of it
}

func (w *when) time(now time.Time) time.Time {
	if w.Back > 0 {
		return now.Add(-w.Back)
	}
	return w.At
}

// Terms is the FTS5 terms of the query's words and phrases, each a quoted
// string, the last word's a prefix: what TestHistoryAndDispatchReadWordsAlike
// compares with Dispatch's reading of the same line.
func (q Query) Terms() []string { return slices.Clone(q.terms) }

// The filters a line takes, by name; any other name:value is a word.
var filterNames = []string{"class", "kind", "outcome", "project", "repo", "host", "agent", "crew", "run", "ticket", "session", "since", "until"}

// classWords are the class filter's values: the classes and the words the
// app shows for two of them.
var classWords = map[string]string{
	ClassSaid: ClassSaid, "conversation": ClassSaid,
	ClassDecided: ClassDecided, "decision": ClassDecided,
	ClassChecked: ClassChecked, ClassRecord: ClassRecord,
}

var (
	outcomePattern = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
	projectPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,5}$`)
	agentPattern   = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	// crewPattern and runPattern are a crew's id and a run's (a crew's id,
	// a dash and eight hex digits), as internal/crew has them (ValidID,
	// ValidRunID), copied since the index imports nothing of the module.
	crewPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	runPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}-[0-9a-f]{8}$`)
	ticketPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,5}-[1-9][0-9]{0,17}$`)
	sessionPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
	spanPattern    = regexp.MustCompile(`^([1-9][0-9]{0,4})([hdw])$`)
)

// ParseQuery reads a person's line: words, "quoted phrases" and filters
// (name:value, a comma between alternatives), at most MaxQuery bytes and
// MaxTerms of them. Words are read as Dispatch's search reads them
// (matchTerms): a word with no letter or digit dropped, every other
// quoted so that OR, NOT, a:b or CND-12 is a word to match and never
// FTS5 syntax, a word repeated kept once, the last a prefix; a phrase is
// one term, its words in order. Every term must match, in the title, the
// summary or the body. A name the filters do not take makes the whole
// name:value a word (http://x is searched for); a filter's value that is
// malformed is ErrQuery, with the words.
func ParseQuery(line string) (Query, error) {
	var q Query
	if len(line) > MaxQuery {
		return q, badQuery("the search is %d bytes, more than %d", len(line), MaxQuery)
	}
	if !utf8.ValidString(line) {
		return q, badQuery("the search is not valid UTF-8")
	}
	var words, phrases []string
	typed, count := false, 0
	sep := func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }
	for rest := line; ; {
		rest = strings.TrimLeftFunc(rest, sep)
		if rest == "" {
			break
		}
		if rest[0] == '"' {
			// A phrase runs to the next quote, or to the end of the line.
			body, after, _ := strings.Cut(rest[1:], `"`)
			rest = after
			typed = true
			if strings.IndexFunc(body, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
				continue
			}
			count++
			phrases = append(phrases, `"`+strings.Join(strings.FieldsFunc(body, sep), " ")+`"`)
			continue
		}
		end := strings.IndexFunc(rest, sep)
		if end < 0 {
			end = len(rest)
		}
		token := rest[:end]
		rest = rest[end:]
		if name, value, ok := strings.Cut(token, ":"); ok && slices.Contains(filterNames, strings.ToLower(name)) {
			count++
			if err := q.addFilter(strings.ToLower(name), value); err != nil {
				return Query{}, err
			}
			continue
		}
		typed = true
		words = append(words, token)
	}
	terms, ok := matchTerms(words)
	if ok {
		count += len(terms)
	}
	if count > MaxTerms {
		return Query{}, badQuery("the search has %d terms, more than %d", count, MaxTerms)
	}
	q.terms = append(terms, phrases...)
	q.none = typed && len(q.terms) == 0 && len(q.filters) == 0 && q.since == nil && q.until == nil
	return q, nil
}

// addFilter checks a filter's value and adds it.
func (q *Query) addFilter(name, value string) error {
	if name == "since" || name == "until" {
		w, err := parseWhen(name, value)
		if err != nil {
			return err
		}
		at := &q.since
		if name == "until" {
			at = &q.until
		}
		if *at != nil {
			return badQuery("%s: is given twice", name)
		}
		*at = w
		return nil
	}
	values := strings.Split(value, ",")
	if len(values) > MaxFilterValues {
		return badQuery("%s: names at most %d values, not %d", name, MaxFilterValues, len(values))
	}
	f := filter{Name: name}
	for _, v := range values {
		if v == "" {
			return badQuery("%s: has an empty value", name)
		}
		var ok bool
		switch name {
		case "class":
			v, ok = classWords[strings.ToLower(v)]
		case "kind":
			v = strings.ToLower(v)
			_, ok = classOf(v)
		case "outcome":
			v = strings.ToLower(v)
			ok = outcomePattern.MatchString(v)
		case "project":
			v = strings.ToUpper(v)
			ok = projectPattern.MatchString(v)
		case "repo":
			v = strings.Trim(v, "/")
			ok = v != ""
		case "host":
			ok = len(v) <= MaxField
		case "agent":
			v = strings.ToLower(v)
			ok = agentPattern.MatchString(v)
		case "crew":
			v = strings.ToLower(v)
			ok = crewPattern.MatchString(v)
		case "run":
			v = strings.ToLower(v)
			ok = runPattern.MatchString(v)
		case "ticket":
			v = strings.ToUpper(v)
			ok = ticketPattern.MatchString(v)
		case "session":
			v = strings.ToLower(v)
			ok = sessionPattern.MatchString(v)
		}
		if !ok {
			return badQuery("%s:%s is not a value of %s:", name, v, name)
		}
		f.Values = append(f.Values, v)
	}
	q.filters = append(q.filters, f)
	return nil
}

// parseWhen reads a since or until: a span back from now (24h, 7d, 2w),
// a day (2026-11-01) or a time in RFC 3339.
func parseWhen(name, v string) (*when, error) {
	if m := spanPattern.FindStringSubmatch(strings.ToLower(v)); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
		return &when{Back: time.Duration(n) * unit}, nil
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return &when{At: t, Date: true}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return &when{At: t.UTC()}, nil
	}
	return nil, badQuery("%s:%s is not a time: give 24h, 7d, 2w, a day (2026-11-01) or a time (2026-11-01T09:00:00Z)", name, v)
}

// matchTerms is Dispatch's reading of a search's words
// (internal/dispatch/query.go, matchTerms), kept here as its own copy
// since the index imports nothing else of the module, over the words a
// line holds outside its phrases and filters: a word with no letter or
// digit dropped; every other a quoted string with its '"' doubled; a word
// kept once however often the line holds it, as the tokenizer reads it
// (termKey); the last a prefix, unless an earlier word is the same. ok is
// false when words were given and none is kept.
func matchTerms(fields []string) (terms []string, ok bool) {
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

func quote(term string) string { return `"` + strings.ReplaceAll(term, `"`, `""`) + `"` }

// termKey is a term as the unicode61 tokenizer reads it, as far as Go can
// tell without it (Dispatch's termKey): ASCII letters folded to lower
// case, every other ASCII character a separator, a run of them one;
// characters past ASCII kept as typed.
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

// digest is what a cursor binds the query by: its terms, filters and
// times as parsed.
func (q Query) digest() string {
	b, _ := json.Marshal(struct {
		T []string `json:"t"`
		N bool     `json:"n"`
		F []filter `json:"f"`
		S *when    `json:"s"`
		U *when    `json:"u"`
	}{q.terms, q.none, q.filters, q.since, q.until})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// conds is the query's filters as conditions over the entries, e, every
// value bound as an argument.
func (q Query) conds(now time.Time) (conds []string, args []any) {
	in := func(col string, vs []string) string {
		for _, v := range vs {
			args = append(args, v)
		}
		return col + " IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(vs)), ", ") + ")"
	}
	for _, f := range q.filters {
		switch f.Name {
		case "class", "kind", "outcome", "project", "agent", "crew", "run", "ticket":
			col := map[string]string{"class": "e.class", "kind": "e.kind", "outcome": "e.outcome", "project": "e.project",
				"agent": "e.agent", "crew": "e.crew", "run": "e.run", "ticket": "e.ticket"}[f.Name]
			conds = append(conds, in(col, f.Values))
		case "host":
			conds = append(conds, in("e.host COLLATE NOCASE", f.Values))
		case "repo":
			// Words of the repository's path: whole segments, in order.
			var ors []string
			for _, v := range f.Values {
				ors = append(ors, "instr('/' || e.repo || '/', ?) > 0")
				args = append(args, "/"+v+"/")
			}
			conds = append(conds, "("+strings.Join(ors, " OR ")+")")
		case "session":
			refs := make([]string, len(f.Values))
			for i, v := range f.Values {
				refs[i] = "session:" + v
			}
			a := in("e.anchor", refs)
			r := in("e.ref", refs)
			conds = append(conds, "("+a+" OR "+r+")")
		}
	}
	if q.since != nil {
		conds = append(conds, "e.at >= ?")
		args = append(args, stamp(q.since.time(now)))
	}
	if q.until != nil {
		t := q.until.time(now)
		if q.until.Date {
			t = t.AddDate(0, 0, 1)
		}
		conds = append(conds, "e.at < ?")
		args = append(args, stamp(t))
	}
	return conds, args
}

// match is the terms as one FTS5 query: each must match.
func (q Query) match() string { return strings.Join(q.terms, " AND ") }

// Page is a page of a search: its entries in the search's order, the
// cursor of the next page ("" on the last), how many entries the search
// names (counted to MaxTotal, TotalCapped past it), the bound it stopped
// at (Cut: CutRanked, CutScanned; "" when it read every match), and the
// index's state: StateOtherVersion, StateMissing or StateChecking, with
// no entries, for an index that holds none of this version or is owed a
// Check.
type Page struct {
	Entries     []Hit  `json:"entries"`
	Next        string `json:"next,omitempty"`
	Total       int    `json:"total"`
	TotalCapped bool   `json:"totalCapped,omitempty"`
	Cut         string `json:"cut,omitempty"`
	State       string `json:"state,omitempty"`
}

// Hit is an entry as a search answers it: with its best column's snippet
// (the match marked [so], at most MaxSnippet bytes), its repeats and its
// links each way (at most 8 on a page).
type Hit struct {
	Entry
	Snippet string  `json:"snippet"`
	Repeats Repeats `json:"repeats"`
	Links   Links   `json:"links"`
}

// Repeats is how many entries of the same digest and class the index
// holds (the entry among them) and in how many anchors: "said in 3
// sessions". Never a change of class.
type Repeats struct {
	Count   int `json:"count"`
	Anchors int `json:"anchors"`
}

// Links are an entry's links each way: Out from its ref, In to it.
type Links struct {
	Out []LinkEnd `json:"out"`
	In  []LinkEnd `json:"in"`
}

// LinkEnd is a link seen from one end: its relation and the other end's
// ref, with the entry that stands for that ref when the index holds one:
// the entry keyed by it, else the first by key of those whose ref it is
// (a command's key is run:<run>#command/<actor>/<id>, its ref
// command:<run>/<id>).
type LinkEnd struct {
	Rel   string  `json:"rel"`
	Ref   string  `json:"ref"`
	Entry *Linked `json:"entry,omitempty"`
}

// Linked is what a link shows of the entry at its other end.
type Linked struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Class   string `json:"class"`
	Outcome string `json:"outcome,omitempty"`
	Title   string `json:"title"`
}

// cursor is where a page ends: the last row's sort values (its score, as
// the float64 it was, which JSON carries exactly; its time; its key), the
// query's digest, and the schema version and generation of the index it
// was read from. As a client holds it, it is opaque: base64url of its
// JSON.
type cursor struct {
	Score float64 `json:"s"`
	At    string  `json:"a"`
	Key   string  `json:"k"`
	Query string  `json:"q"`
	V     int     `json:"v"`
	Gen   int64   `json:"g"`
}

func encodeCursor(c cursor) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(c)
	return base64.RawURLEncoding.EncodeToString(bytes.TrimSpace(b.Bytes()))
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	if len(s) > maxCursor {
		return c, fmt.Errorf("%w: more than %d bytes", ErrCursor, maxCursor)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, ErrCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, ErrCursor
	}
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF {
		return c, ErrCursor
	}
	return c, nil
}

// entryColumns is an entry as scanEntry reads it, from entryFrom.
const entryColumns = `e.id, e.key, e.source, e.kind, e.class, e.ref, e.seq, e.anchor, e.at, e.host, e.host_user, e.hosted, e.where_ran,
	e.project, e.repo, e.tree, e.revision, e.commit_id, e.agent, e.agent_version, e.agent_session, e.crew, e.run, e.ticket,
	e.outcome, e.actor, e.subject_key, e.digest, t.title, t.summary, t.body, e.origin`

// entryFrom is the entries with their texts.
const entryFrom = "entries e JOIN texts t ON t.id = e.id"

// scanEntry reads an entry (entryColumns) after the destinations given.
func scanEntry(rows interface{ Scan(...any) error }, before ...any) (id int64, e Entry, err error) {
	var at string
	dest := append(before, &id, &e.Key, &e.Source, &e.Kind, &e.Class, &e.Ref, &e.Seq, &e.Anchor, &at, &e.Host, &e.HostUser, &e.Hosted, &e.Where,
		&e.Project, &e.Repo, &e.Tree, &e.Revision, &e.Commit, &e.Agent, &e.AgentVersion, &e.AgentSession, &e.Crew, &e.Run, &e.Ticket,
		&e.Outcome, &e.Actor, &e.Subject, &e.Digest, &e.Title, &e.Summary, &e.Body, &e.Origin)
	if err := rows.Scan(dest...); err != nil {
		return 0, Entry{}, err
	}
	e.At = unstamp(at)
	return id, e, nil
}

// Search answers a page of the query. Order: with words, FTS5's
// bm25(entries_fts, 4, 2, 1) (title, summary, body), then newer first;
// with filters alone, newer first; in both, last by the entry's key, so
// the order is total. Class never weighs in it. A search of words reads
// at most MaxScanned of its matches and ranks at most MaxRanked, and says
// so (Page.Cut); past MaxScanned matches its page is newest first. The
// next page is the rows strictly after the cursor's in the same order.
// Every statement reads one snapshot, after the index's version and
// generation: an index of another version, or owed a Check, answers its
// State and no rows, a cursor of another query ErrCursor, one of an
// earlier generation ErrStaleCursor. It reads through the owner's readers,
// never its writer, so no write waits for it.
func (ix *Index) Search(ctx context.Context, q Query) (Page, error) {
	page := Page{Entries: []Hit{}}
	limit := q.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaxLimit {
		return page, badQuery("a page is 1 to %d entries, not %d", MaxLimit, q.Limit)
	}
	var after *cursor
	if q.After != "" {
		c, err := decodeCursor(q.After)
		if err != nil {
			return page, err
		}
		if c.Query != q.digest() {
			return page, fmt.Errorf("%w: it is a page of another search", ErrCursor)
		}
		after = &c
	}
	if err := ix.enter(); err != nil {
		return page, err
	}
	defer ix.leave()
	tx, gen, state, err := ix.readTx(ctx)
	if err != nil {
		return page, err
	}
	if tx != nil {
		defer tx.Rollback()
	}
	if state != "" {
		page.State = state
		return page, nil
	}
	if after != nil && (after.V != Version || after.Gen != gen) {
		return page, ErrStaleCursor
	}
	if q.none {
		return page, nil
	}
	conds, args := q.conds(ix.now())
	words := len(q.terms) > 0
	where := func(extra ...string) string {
		all := append(slices.Clone(conds), extra...)
		if len(all) == 0 {
			return ""
		}
		return " WHERE " + strings.Join(all, " AND ")
	}
	// The page is sorted by its keys alone (p), each entry's id, score,
	// time and key, and only its rows are then read whole: a sort that
	// carried every match's texts would copy them all. A search of words
	// first asks the full-text index for the rowids of its words'
	// matches, newest indexed first, and stops at maxScanned+1 (a walk of
	// the doclist that ORDER BY rowid DESC LIMIT lets FTS5 end there). At
	// most maxScanned: it reads them, newest indexed first, the filters
	// applied, and stops at maxRanked+1 (r); the newest maxRanked of them
	// are scored (m), once, and the page is drawn from m and its total
	// counted from r, as Dispatch's list counts its own. More: the window
	// is the newest maxScanned of those rowids, given back as JSON, and the
	// page is those of its entries that pass the filters, newest first,
	// unscored (with filters, m, counted). So bm25 is computed for at most
	// maxRanked+1 rows and the doclist walked to at most maxScanned+1. A
	// page of filters alone reads the newest first and stops at the page;
	// its total is a count of its own, to MaxTotal and one past it.
	var stmt string
	var pageArgs []any
	order := "ORDER BY p.score, p.at DESC, p.key"
	whole := " FROM p JOIN entries e ON e.id = p.id JOIN texts t ON t.id = p.id "
	var matched int   // the words' matches, to maxScanned+1
	var window string // past maxScanned, the newest maxScanned's rowids as JSON
	andConds := ""
	if len(conds) > 0 {
		andConds = " AND " + strings.Join(conds, " AND ")
	}
	scanned := func() bool { return matched > ix.maxScanned }
	if words {
		rows, err := tx.QueryContext(ctx, "SELECT rowid FROM entries_fts WHERE entries_fts MATCH ? ORDER BY rowid DESC LIMIT ?", q.match(), ix.maxScanned+1)
		if err != nil {
			return page, err
		}
		b := []byte{'['}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return page, err
			}
			if matched++; matched <= ix.maxScanned {
				if matched > 1 {
					b = append(b, ',')
				}
				b = strconv.AppendInt(b, id, 10)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return page, err
		}
		window = string(append(b, ']'))
		past := ""
		var pastArgs []any
		if after != nil {
			past = " WHERE (score > ? OR (score = ? AND (at < ? OR (at = ? AND key > ?))))"
			pastArgs = []any{after.Score, after.Score, after.At, after.At, after.Key}
		}
		if !scanned() {
			r := `SELECT f.rowid AS id, bm25(entries_fts, 4, 2, 1) AS score, e.at AS at, e.key AS key
				FROM entries_fts f CROSS JOIN entries e ON e.id = f.rowid WHERE entries_fts MATCH ?` + andConds + " ORDER BY f.rowid DESC LIMIT ?"
			pageArgs = append(append(append(append(pageArgs, q.match()), args...), ix.maxRanked+1, ix.maxRanked), pastArgs...)
			stmt = "WITH r AS MATERIALIZED (" + r + "), m AS MATERIALIZED (SELECT * FROM r ORDER BY id DESC LIMIT ?), p AS (SELECT * FROM m" + past +
				" ORDER BY score, at DESC, key LIMIT ?) SELECT (SELECT COUNT(*) FROM r), p.score, " + entryColumns + whole + order
		} else if len(conds) == 0 {
			// The total is known (more than maxScanned): the window's
			// newest first, a sort that keeps only the page.
			var extra []string
			pageArgs = append(pageArgs, window)
			if after != nil {
				extra = append(extra, "(e.at < ? OR (e.at = ? AND e.key > ?))")
				pageArgs = append(pageArgs, after.At, after.At, after.Key)
			}
			stmt = "WITH p AS (SELECT e.id AS id, 0.0 AS score, e.at AS at, e.key AS key" + windowFrom + where(extra...) +
				" ORDER BY e.at DESC, e.key LIMIT ?) SELECT -1, p.score, " + entryColumns + whole + order
		} else {
			pageArgs = append(append(append(pageArgs, window), args...), pastArgs...)
			stmt = "WITH m AS MATERIALIZED (SELECT e.id AS id, 0.0 AS score, e.at AS at, e.key AS key" + windowFrom + where() + "), p AS (SELECT * FROM m" + past +
				" ORDER BY at DESC, key LIMIT ?) SELECT (SELECT COUNT(*) FROM m), p.score, " + entryColumns + whole + order
		}
	} else {
		var extra []string
		pageArgs = append(pageArgs, args...)
		if after != nil {
			extra = append(extra, "(e.at < ? OR (e.at = ? AND e.key > ?))")
			pageArgs = append(pageArgs, after.At, after.At, after.Key)
		}
		stmt = "WITH p AS (SELECT e.id AS id, 0.0 AS score, e.at AS at, e.key AS key FROM entries e" + where(extra...) +
			" ORDER BY e.at DESC, e.key LIMIT ?) SELECT -1, p.score, " + entryColumns + whole + order
	}
	rows, err := tx.QueryContext(ctx, stmt, append(pageArgs, limit+1)...)
	if err != nil {
		return page, err
	}
	var ids []int64
	var scores []float64
	total := -1
	for rows.Next() {
		var s float64
		id, e, err := scanEntry(rows, &total, &s)
		if err != nil {
			rows.Close()
			return page, err
		}
		if len(page.Entries) == limit {
			last := page.Entries[limit-1]
			page.Next = encodeCursor(cursor{Score: scores[limit-1], At: stamp(last.At), Key: last.Key, Query: q.digest(), V: Version, Gen: gen})
			break
		}
		ids, scores = append(ids, id), append(scores, s)
		page.Entries = append(page.Entries, Hit{Entry: e})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page, err
	}
	// The total: with words and no filters, the probe's count of the
	// matches (past maxScanned, at least that many; a count of its own
	// when that is under MaxTotal, as a test's maxScanned is); with
	// filters, what the page's statement counted when that is every
	// passing match (the matches read whole and at most maxRanked passing;
	// past maxScanned, those of the window that pass), else a count of its
	// own (the matches that pass, of which there are at most maxScanned;
	// the window's); with filters alone, a count of its own. Each count to
	// MaxTotal and one past it.
	countOwn := func() error {
		count, countArgs := "SELECT 1 FROM entries e"+where(), args
		switch {
		case words && len(conds) == 0:
			count, countArgs = "SELECT 1 FROM entries_fts WHERE entries_fts MATCH ?", []any{q.match()}
		case words && !scanned():
			count = "SELECT 1 FROM entries_fts f CROSS JOIN entries e ON e.id = f.rowid WHERE entries_fts MATCH ?" + andConds
			countArgs = append([]any{q.match()}, args...)
		case words:
			count = "SELECT 1" + windowFrom + where()
			countArgs = append([]any{window}, args...)
		}
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+count+" LIMIT ?)", append(countArgs, MaxTotal+1)...).Scan(&page.Total)
	}
	page.Total = total
	var counted error
	switch {
	case !words:
		if total < 0 {
			counted = countOwn()
		}
	case len(conds) == 0 && (!scanned() || matched > MaxTotal):
		page.Total = matched
	case len(conds) == 0:
		counted = countOwn()
	case !scanned() && total >= 0 && total <= ix.maxRanked:
	case !scanned() || total < 0:
		counted = countOwn()
	}
	if counted != nil {
		return page, counted
	}
	switch {
	case !words:
	case scanned():
		page.Cut = CutScanned
	case page.Total > ix.maxRanked:
		page.Cut = CutRanked
	}
	if page.Total > MaxTotal {
		page.Total, page.TotalCapped = MaxTotal, true
	}
	if err := decorate(ctx, tx, page.Entries, ids, q.match()); err != nil {
		return page, err
	}
	return page, nil
}

// decorate gives each hit its snippet (from the match when there is one:
// its best column with the match marked; else its summary or body), its
// repeats and its links, each a statement for the whole page.
func decorate(ctx context.Context, tx *sql.Tx, hits []Hit, ids []int64, match string) error {
	if len(hits) == 0 {
		return nil
	}
	snippets := map[int64]string{}
	if match != "" {
		// One pass of the match over the page's range of rowids (a rowid
		// list would run the match once for each), the snippet drawn for
		// the page's rows alone (the unary plus keeps the list out of
		// the match).
		args := []any{match, slices.Min(ids), slices.Max(ids)}
		for _, id := range ids {
			args = append(args, id)
		}
		err := each(ctx, tx, `SELECT rowid, snippet(entries_fts, -1, char(57344), char(57345), '…', 24) FROM entries_fts
			WHERE entries_fts MATCH ? AND rowid BETWEEN ? AND ? AND +rowid IN (`+marks(len(ids))+`)`, args, func(rows *sql.Rows) error {
			var id int64
			var s string
			if err := rows.Scan(&id, &s); err != nil {
				return err
			}
			snippets[id] = s
			return nil
		})
		if err != nil {
			return err
		}
	}
	type group struct{ digest, class string }
	repeats := map[group]Repeats{}
	var args []any
	refs := map[string]bool{}
	for _, h := range hits {
		args = append(args, h.Digest)
		refs[h.Ref] = true
	}
	err := each(ctx, tx, `SELECT digest, class, COUNT(*), COUNT(DISTINCT anchor) FROM entries
		WHERE digest IN (`+marks(len(args))+`) GROUP BY digest, class`, args, func(rows *sql.Rows) error {
		var g group
		var r Repeats
		if err := rows.Scan(&g.digest, &g.class, &r.Count, &r.Anchors); err != nil {
			return err
		}
		repeats[g] = r
		return nil
	})
	if err != nil {
		return err
	}
	links, err := readLinks(ctx, tx, slices.Sorted(maps.Keys(refs)), pageLinks)
	if err != nil {
		return err
	}
	for i := range hits {
		h := &hits[i]
		if s, ok := snippets[ids[i]]; ok {
			h.Snippet = clipSnippet(s, MaxSnippet)
		} else if h.Snippet = h.Summary; h.Snippet == "" {
			h.Snippet = Clip(h.Body, MaxSnippet)
		} else {
			h.Snippet = Clip(h.Snippet, MaxSnippet)
		}
		h.Repeats = repeats[group{h.Digest, h.Class}]
		h.Links = Links{Out: []LinkEnd{}, In: []LinkEnd{}}
		if l, ok := links[h.Ref]; ok {
			h.Links = l
		}
	}
	return nil
}

// windowFrom is a search's window past maxScanned matches (Search): the
// entries of the rowids its probe read, given as a JSON array. CROSS JOIN
// keeps the array the outer loop: with entries outer (a filter's index
// chosen) the array would be read again for every entry. The words'
// statements join the same way, the full-text index outer, so their
// LIMIT ends its walk.
const windowFrom = " FROM json_each(?) j CROSS JOIN entries e ON e.id = j.value"

// snipOpen and snipClose are where snippet() marks a match (U+E000 and
// U+E001, which no text of the index is expected to hold), so the match
// can be found in a text that holds brackets of its own; clipSnippet
// writes them as [ and ].
const (
	snipOpen  = "\uE000"
	snipClose = "\uE001"
)

var markWords = strings.NewReplacer(snipOpen, "[", snipClose, "]")

// clipSnippet is snippet()'s text with its marks as [ and ], at most n
// bytes: whole when it fits, else the window of it around its first
// match, centred on it as far as the text allows, cut at rune boundaries
// and never through a mark, with … where it was cut. snippet() bounds its
// window in tokens, not bytes, so long tokens before the match (hashes, a
// path, a CJK sentence the tokenizer reads as one) would put it past a
// cut from the start.
func clipSnippet(s string, n int) string {
	s = strings.ToValidUTF8(s, replacement)
	if f := markWords.Replace(s); len(f) <= n {
		return f
	}
	o := strings.Index(s, snipOpen)
	if o < 0 {
		return Clip(markWords.Replace(s), n)
	}
	c := len(s)
	if i := strings.Index(s[o:], snipClose); i >= 0 {
		c = o + i + len(snipClose)
	}
	const cut = "…"
	budget := n - 2*len(cut)
	if c-o > budget {
		// The match alone is longer than the bound: its start kept.
		return Clip(markWords.Replace(s[o:c]), n-len(cut)) + cut
	}
	start := max(o-(budget-(c-o))/2, 0)
	end := min(start+budget, len(s))
	start = max(min(start, end-budget), 0)
	for start < o && !utf8.RuneStart(s[start]) {
		start++
	}
	for end > c && end < len(s) && !utf8.RuneStart(s[end]) {
		end--
	}
	// A mark the window's start or end falls in is left out whole.
	if i, j := strings.Index(s[start:o], snipClose), strings.Index(s[start:o], snipOpen); i >= 0 && (j < 0 || i < j) {
		start += i + len(snipClose)
	}
	if i, j := strings.LastIndex(s[c:end], snipOpen), strings.LastIndex(s[c:end], snipClose); i >= 0 && i > j {
		end = c + i
	}
	out := markWords.Replace(s[start:end])
	if start > 0 {
		out = cut + out
	}
	if end < len(s) {
		out += cut
	}
	return out
}

// marks is n placeholders.
func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

// each runs a query and calls fn on each of its rows.
func each(ctx context.Context, tx *sql.Tx, stmt string, args []any, fn func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, stmt, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// readLinks reads at most limit links each way of each ref, each with the
// entry that stands for its other end when the index holds one (LinkEnd):
// one statement a way.
func readLinks(ctx context.Context, tx *sql.Tx, refs []string, limit int) (map[string]Links, error) {
	out := map[string]Links{}
	for _, r := range refs {
		out[r] = Links{Out: []LinkEnd{}, In: []LinkEnd{}}
	}
	if len(refs) == 0 {
		return out, nil
	}
	for _, way := range []struct {
		near, far string
		in        bool
	}{{"from_ref", "to_ref", false}, {"to_ref", "from_ref", true}} {
		args := make([]any, 0, len(refs)+1)
		for _, r := range refs {
			args = append(args, r)
		}
		args = append(args, limit)
		stmt := `SELECT near, rel, far, okey, kind, class, outcome, title FROM (
			SELECT l.` + way.near + ` AS near, l.rel AS rel, l.` + way.far + ` AS far, o.key AS okey, o.kind AS kind, o.class AS class,
				o.outcome AS outcome, ot.title AS title,
				row_number() OVER (PARTITION BY l.` + way.near + ` ORDER BY l.rel, l.` + way.far + `) AS n
				FROM links l LEFT JOIN entries o ON o.id = COALESCE(
					(SELECT x.id FROM entries x WHERE x.key = l.` + way.far + `),
					(SELECT x.id FROM entries x WHERE x.ref = l.` + way.far + ` ORDER BY x.key LIMIT 1))
				LEFT JOIN texts ot ON ot.id = o.id
			WHERE l.` + way.near + ` IN (` + marks(len(refs)) + `)) WHERE n <= ? ORDER BY near, rel, far`
		err := each(ctx, tx, stmt, args, func(rows *sql.Rows) error {
			var near string
			var end LinkEnd
			var key, kind, class, outcome, title sql.NullString
			if err := rows.Scan(&near, &end.Rel, &end.Ref, &key, &kind, &class, &outcome, &title); err != nil {
				return err
			}
			if key.Valid {
				end.Entry = &Linked{Key: key.String, Kind: kind.String, Class: class.String, Outcome: outcome.String, Title: title.String}
			}
			l := out[near]
			if way.in {
				l.In = append(l.In, end)
			} else {
				l.Out = append(l.Out, end)
			}
			out[near] = l
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Entry reads one entry by its key, with its repeats and its links (at
// most 8 each way; Links reads more): ErrGone for a key the index no
// longer holds, never another entry.
func (ix *Index) Entry(ctx context.Context, key string) (Hit, error) {
	var hit Hit
	err := ix.read(ctx, true, func(tx *sql.Tx, _ int64) error {
		id, e, err := scanEntry(tx.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM "+entryFrom+" WHERE e.key = ?", key))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGone
		}
		if err != nil {
			return err
		}
		hits := []Hit{{Entry: e}}
		if err := decorate(ctx, tx, hits, []int64{id}, ""); err != nil {
			return err
		}
		hit = hits[0]
		return nil
	})
	return hit, err
}

// Links reads the links of a ref each way, at most limit (1 to MaxLinks)
// each.
func (ix *Index) Links(ctx context.Context, ref string, limit int) (Links, error) {
	if limit < 1 || limit > MaxLinks {
		limit = MaxLinks
	}
	var links Links
	err := ix.read(ctx, true, func(tx *sql.Tx, _ int64) error {
		all, err := readLinks(ctx, tx, []string{ref}, limit)
		links = all[ref]
		return err
	})
	return links, err
}

// The scopes of Similar.
const (
	ScopeRepo = "repo" // the entry's repository (its project's when it has one)
	ScopeAll  = "all"  // every repository
)

// Similar answers "did another crew solve this" for a receipt: the
// receipts of the same command (its subject, the SHA-256 of its argv)
// outside the entry's run chain (chain, given by the caller; the entry's
// own run always among it), in the entry's repository (its project when
// it has one, else its repository's identity; none when it has neither)
// unless scope is ScopeAll, ranked: first those whose body also holds the
// entry's distinctive line (the last non-empty line of a failed receipt's
// log, as a phrase), passed before failed, then the rest of the same
// command's, passed before failed, each group newest first; then, in the
// same scope, receipts of other commands that hold the line, newest
// first. At most limit (1 to MaxSimilar). ErrGone for a key the index no
// longer holds; nothing for an entry that is not a receipt.
func (ix *Index) Similar(ctx context.Context, key, scope string, chain []string, limit int) ([]Hit, error) {
	if scope == "" {
		scope = ScopeRepo
	}
	if scope != ScopeRepo && scope != ScopeAll {
		return nil, badQuery("the scope %q is neither %s nor %s", scope, ScopeRepo, ScopeAll)
	}
	if limit < 1 || limit > MaxSimilar {
		limit = MaxSimilar
	}
	hits := []Hit{}
	err := ix.read(ctx, true, func(tx *sql.Tx, _ int64) error {
		_, e, err := scanEntry(tx.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM "+entryFrom+" WHERE e.key = ?", key))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGone
		}
		if err != nil {
			return err
		}
		if e.Subject == "" || !slices.Contains(receiptKinds, e.Kind) {
			return nil
		}
		line := ""
		if e.Outcome == "failed" {
			line = distinctiveLine(e.Body)
		}
		hitsLine := "SELECT NULL WHERE 0"
		var args []any
		if line != "" {
			hitsLine = "SELECT rowid FROM entries_fts WHERE entries_fts MATCH ?"
			args = append(args, "body : "+line)
		}
		kinds := strings.TrimSuffix(strings.Repeat("?, ", len(receiptKinds)), ", ")
		stmt := `WITH line AS MATERIALIZED (` + hitsLine + `)
			SELECT CASE WHEN e.subject_key = ? THEN (CASE WHEN e.id IN (SELECT * FROM line) THEN 0 ELSE 2 END)
				+ (CASE WHEN e.outcome = 'passed' THEN 0 ELSE 1 END) ELSE 4 END AS grp, ` + entryColumns + `
			FROM ` + entryFrom + ` WHERE e.kind IN (` + kinds + `) AND e.key != ? AND (e.subject_key = ? OR e.id IN (SELECT * FROM line))`
		args = append(args, e.Subject)
		for _, k := range receiptKinds {
			args = append(args, k)
		}
		args = append(args, e.Key, e.Subject)
		runs := slices.Clone(chain)
		if e.Run != "" {
			runs = append(runs, e.Run)
		}
		if len(runs) > 0 {
			stmt += " AND e.run NOT IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(runs)), ", ") + ")"
			for _, r := range runs {
				args = append(args, r)
			}
		}
		if scope == ScopeRepo {
			switch {
			case e.Project != "":
				stmt += " AND e.project = ?"
				args = append(args, e.Project)
			case e.Repo != "":
				stmt += " AND e.repo = ?"
				args = append(args, e.Repo)
			default:
				return nil
			}
		}
		stmt += " ORDER BY grp, e.at DESC, e.key LIMIT ?"
		rows, err := tx.QueryContext(ctx, stmt, append(args, limit)...)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var grp int
			id, h, err := scanEntry(rows, &grp)
			if err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
			hits = append(hits, Hit{Entry: h})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return decorate(ctx, tx, hits, ids, "")
	})
	return hits, err
}

// distinctiveLine is the last non-empty line of a log as an FTS5 phrase
// ("" when it holds no letter or digit), cut at a space to at most 512
// bytes.
func distinctiveLine(body string) string {
	lines := strings.Split(strings.TrimRightFunc(body, unicode.IsSpace), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if strings.IndexFunc(last, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
		return ""
	}
	if len(last) > MaxRef {
		last = Clip(last, MaxRef)
		if i := strings.LastIndexFunc(last, unicode.IsSpace); i > 0 {
			last = last[:i]
		}
	}
	return quote(last)
}
