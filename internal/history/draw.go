package history

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A restricted read (Query.Newest) answers from its restriction's rows
// alone, and reads no other: its rows are drawn by their kind and the
// column that places them (Clause.Place), each such place through an index
// of its kind, its value and its time (placeColumns), newest first, and
// merged; each batch of drawn rows is checked against the restriction
// again and the line's filters, and its words are matched against those
// rows' own texts, copied into a full-text table of their own made the
// index's way (ftsTokens), so the index's tokenizer decides what matches
// and no other row's words are walked. A read draws at most Examine rows
// (MaxExamine), which is its whole cost, and says so (CutExamined) when
// they hold less than a page.

// The bounds of a restricted read.
const (
	// MaxExamine bounds the rows one restricted read draws, and is what
	// one that names none draws at most.
	MaxExamine = 1000
	// MaxPlaces bounds the places one draws from, a kind and a value of
	// its clause's column each.
	MaxPlaces = 512
	// drawBatch is how many drawn rows are checked and matched at a time,
	// drawChunk the most one place is asked for at a time.
	drawBatch = 100
	drawChunk = 64
)

// ftsTokens is how the index's full-text table reads words, which the
// table a restricted read matches its rows' words in shares.
const ftsTokens = "tokenize = 'unicode61 remove_diacritics 2', prefix = '2 3'"

// placeColumns are the columns a clause's rows are drawn by (Place), each
// with the index that holds the entries by kind, that column's value
// (expr, an expression over the row; none for "", every row of a kind),
// time and key: a draw reads that index's range of one kind and one value
// and nothing else.
var placeColumns = map[string]struct{ index, expr string }{
	"":               {"entries_draw", ""},
	"tree":           {"entries_draw_tree", "tree"},
	"anchor":         {"entries_draw_anchor", "anchor"},
	"run":            {"entries_draw_run", "run"},
	"run.crew":       {"entries_draw_crew", "substr(run, 1, max(length(run) - 9, 0))"},
	"ticket.project": {"entries_draw_project", "substr(ticket, 1, instr(ticket, '-') - 1)"},
}

// SetDrawSeam gives f the id of every row a restricted read draws from
// now on (nil: none): a test's count of what a read reads.
func (ix *Index) SetDrawSeam(f func(id int64)) {
	if f == nil {
		ix.drawSeam.Store(nil)
		return
	}
	ix.drawSeam.Store(&f)
}

// drawRow is a row drawn: its id, time and key, as the index keeps them.
type drawRow struct {
	id      int64
	at, key string
}

// before reports whether a comes before b newest first: later, or at the
// same time with the greater key.
func (a drawRow) before(b drawRow) bool {
	return a.at > b.at || (a.at == b.at && a.key > b.key)
}

// drawPlace is one kind and one value of its clause's column: its
// statement over its index, the rows it gave not yet merged, the last it
// gave (where it reads on from) and whether it has given its last.
type drawPlace struct {
	stmt  string
	args  []any
	buf   []drawRow
	last  *drawRow
	done  bool
	chunk int
}

// drawer merges its places' rows newest first, drawing at most budget
// rows in all. seam, when set, is given every row drawn.
type drawer struct {
	ctx    context.Context
	tx     *sql.Tx
	places []*drawPlace
	budget int
	drawn  int
	seam   func(id int64)
}

// fill asks p for its next rows, as many as its chunk and the budget let.
func (d *drawer) fill(p *drawPlace) error {
	n := min(p.chunk, d.budget-d.drawn)
	stmt, args := p.stmt, slices.Clone(p.args)
	if p.last != nil {
		stmt += " AND (at, key) < (?, ?)"
		args = append(args, p.last.at, p.last.key)
	}
	rows, err := d.tx.QueryContext(d.ctx, stmt+" ORDER BY at DESC, key DESC LIMIT ?", append(args, n)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	got := 0
	for rows.Next() {
		var r drawRow
		if err := rows.Scan(&r.id, &r.at, &r.key); err != nil {
			return err
		}
		got++
		p.buf = append(p.buf, r)
		p.last = &r
		if d.seam != nil {
			d.seam(r.id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	d.drawn += got
	p.done = got < n
	p.chunk = min(2*p.chunk, drawChunk)
	return nil
}

// next is the next row newest first; ok false with cut false when every
// place has given its last, with cut true when the budget is spent before
// the next row is known.
func (d *drawer) next() (row drawRow, ok, cut bool, err error) {
	for _, p := range d.places {
		if len(p.buf) > 0 || p.done {
			continue
		}
		if d.drawn >= d.budget {
			return drawRow{}, false, true, nil
		}
		if err := d.fill(p); err != nil {
			return drawRow{}, false, false, err
		}
	}
	var best *drawPlace
	for _, p := range d.places {
		if len(p.buf) > 0 && (best == nil || p.buf[0].before(best.buf[0])) {
			best = p
		}
	}
	if best == nil {
		return drawRow{}, false, false, nil
	}
	row, best.buf = best.buf[0], best.buf[1:]
	return row, true, false, nil
}

// window is the query's since and until as the times its rows are drawn
// within ("" for none).
func (q Query) window(now time.Time) (since, until string) {
	if q.since != nil {
		since = stamp(q.since.time(now))
	}
	if q.until != nil {
		t := q.until.time(now)
		if q.until.Date {
			t = t.AddDate(0, 0, 1)
		}
		until = stamp(t)
	}
	return since, until
}

// kindsOf is the kinds the line's kind filters leave, nil for no filter.
func (q Query) kindsOf() map[string]bool {
	var kinds map[string]bool
	for _, f := range q.filters {
		if f.Name != "kind" {
			continue
		}
		next := map[string]bool{}
		for _, v := range f.Values {
			if kinds == nil || kinds[v] {
				next[v] = true
			}
		}
		kinds = next
	}
	return kinds
}

// places is every place q's restriction draws from: each kind of each
// clause the line's kind filters leave, by each value of its column,
// from the line's since (its until is where each reads from, searchDrawn).
func (q Query) places(now time.Time) []*drawPlace {
	since, _ := q.window(now)
	kinds := q.kindsOf()
	var out []*drawPlace
	for _, c := range q.Restrict.Clauses {
		col, values := "", []string{""}
		if c.Place != nil {
			col, values = c.Place.Column, c.Place.Values
		}
		pc := placeColumns[col]
		where := "SELECT id, at, key FROM entries INDEXED BY " + pc.index + " WHERE kind = ?"
		if pc.expr != "" {
			where += " AND " + pc.expr + " = ?"
		}
		if since != "" {
			where += " AND at >= ?"
		}
		seenKind := map[string]bool{}
		for _, k := range c.Kinds {
			if seenKind[k] || (kinds != nil && !kinds[k]) {
				continue
			}
			seenKind[k] = true
			seenValue := map[string]bool{}
			for _, v := range values {
				if seenValue[v] {
					continue
				}
				seenValue[v] = true
				args := []any{k}
				if pc.expr != "" {
					args = append(args, v)
				}
				if since != "" {
					args = append(args, since)
				}
				out = append(out, &drawPlace{stmt: where, args: args, chunk: 1})
			}
		}
	}
	return out
}

// idList is ids as a JSON array, for json_each.
func idList(ids []int64) string {
	b := []byte{'['}
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, id, 10)
	}
	return string(append(b, ']'))
}

// wordsTable is where a restricted read matches its drawn rows' words:
// a full-text table of their texts alone, in memory, made the index's
// way, emptied for each batch.
type wordsTable struct {
	db   *sql.DB
	conn *sql.Conn
}

func openWords(ctx context.Context) (*wordsTable, error) {
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err == nil {
		_, err = conn.ExecContext(ctx, "CREATE VIRTUAL TABLE words USING fts5(title, summary, body, "+ftsTokens+")")
	}
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		db.Close()
		return nil, err
	}
	return &wordsTable{db: db, conn: conn}, nil
}

func (w *wordsTable) close() {
	w.conn.Close()
	w.db.Close()
}

// match is the snippet of each row of ids the match holds, its texts read
// from tx.
func (w *wordsTable) match(ctx context.Context, tx *sql.Tx, ids []int64, match string) (map[int64]string, error) {
	if _, err := w.conn.ExecContext(ctx, "DELETE FROM words"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT t.id, t.title, t.summary, t.body FROM json_each(?) j CROSS JOIN texts t ON t.id = j.value", idList(ids))
	if err != nil {
		return nil, err
	}
	type text struct {
		id                   int64
		title, summary, body string
	}
	var texts []text
	for rows.Next() {
		var x text
		if err := rows.Scan(&x.id, &x.title, &x.summary, &x.body); err != nil {
			rows.Close()
			return nil, err
		}
		texts = append(texts, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, x := range texts {
		if _, err := w.conn.ExecContext(ctx, "INSERT INTO words (rowid, title, summary, body) VALUES (?, ?, ?, ?)", x.id, x.title, x.summary, x.body); err != nil {
			return nil, err
		}
	}
	out := map[int64]string{}
	m, err := w.conn.QueryContext(ctx, "SELECT rowid, snippet(words, -1, char(57344), char(57345), '…', 24) FROM words WHERE words MATCH ?", match)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	for m.Next() {
		var id int64
		var s string
		if err := m.Scan(&id, &s); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, m.Err()
}

// searchDrawn answers a Newest query (Search) from its restriction's rows:
// drawn newest first, at most q.Examine of them, each batch checked
// against the restriction and the line's filters and its words matched
// against its own rows' texts; the page is the first limit that pass, its
// Next after the last of them, or, when the draw stopped at its bound
// first (CutExamined), after the last row drawn and checked. Nothing is
// counted (no Total), and a hit carries its snippet alone.
func (ix *Index) searchDrawn(ctx context.Context, tx *sql.Tx, q Query, after *cursor, limit int, gen int64) (Page, error) {
	page := Page{Entries: []Hit{}}
	now := ix.now()
	places := q.places(now)
	if len(places) > MaxPlaces {
		return page, badQuery("the read draws from %d places, more than %d: name kinds", len(places), MaxPlaces)
	}
	// Each place reads on from the position, past the line's until: the
	// older of the two (an until is a row's time no row of it reaches).
	var from *drawRow
	if after != nil {
		from = &drawRow{at: after.At, key: after.Key}
	}
	if _, until := q.window(now); until != "" && (from == nil || from.before(drawRow{at: until})) {
		from = &drawRow{at: until}
	}
	for _, p := range places {
		if from != nil {
			at := *from
			p.last = &at
		}
	}
	budget := q.Examine
	if budget == 0 {
		budget = MaxExamine
	}
	d := &drawer{ctx: ctx, tx: tx, places: places, budget: budget}
	if f := ix.drawSeam.Load(); f != nil {
		d.seam = *f
	}
	rsql, rargs := q.Restrict.sql()
	conds, cargs := q.conds(now)
	check := "SELECT e.id FROM json_each(?) j CROSS JOIN entries e ON e.id = j.value WHERE " + strings.Join(append([]string{rsql}, conds...), " AND ")
	checkArgs := append(rargs, cargs...)
	var words *wordsTable
	if len(q.terms) > 0 {
		var err error
		if words, err = openWords(ctx); err != nil {
			return page, err
		}
		defer words.close()
	}
	var hits []drawRow
	snippets := map[int64]string{}
	var last *drawRow
	cut, full := false, false
	for !cut && !full {
		var batch []drawRow
		ended := false
		for len(batch) < drawBatch {
			row, ok, c, err := d.next()
			if err != nil {
				return page, err
			}
			if c {
				cut = true
				break
			}
			if !ok {
				ended = true
				break
			}
			batch = append(batch, row)
		}
		if len(batch) > 0 {
			ids := make([]int64, len(batch))
			for i, r := range batch {
				ids[i] = r.id
			}
			kept := map[int64]bool{}
			err := each(ctx, tx, check, append([]any{idList(ids)}, checkArgs...), func(rows *sql.Rows) error {
				var id int64
				if err := rows.Scan(&id); err != nil {
					return err
				}
				kept[id] = true
				return nil
			})
			if err != nil {
				return page, err
			}
			matched := map[int64]string{}
			if words != nil {
				var keptIDs []int64
				for _, id := range ids {
					if kept[id] {
						keptIDs = append(keptIDs, id)
					}
				}
				if len(keptIDs) > 0 {
					if matched, err = words.match(ctx, tx, keptIDs, q.match()); err != nil {
						return page, err
					}
				}
			}
			for _, r := range batch {
				last = &r
				if !kept[r.id] {
					continue
				}
				if words != nil {
					s, ok := matched[r.id]
					if !ok {
						continue
					}
					snippets[r.id] = s
				}
				hits = append(hits, r)
				if len(hits) == limit {
					full = true
					break
				}
			}
		}
		if ended {
			break
		}
	}
	page.Drawn = d.drawn
	digest := q.digest()
	at := func(r drawRow) string {
		return encodeCursor(cursor{At: r.at, Key: r.key, Query: digest, V: Version, Gen: gen})
	}
	switch {
	case full:
		page.Next = at(hits[len(hits)-1])
	case cut:
		page.Cut = CutExamined
		if last != nil {
			page.Next = at(*last)
		} else {
			page.Next = q.After
		}
	}
	if len(hits) == 0 {
		return page, nil
	}
	ids := make([]int64, len(hits))
	for i, r := range hits {
		ids[i] = r.id
	}
	entries := map[int64]Entry{}
	err := each(ctx, tx, "SELECT 0, 0.0, "+entryColumns+" FROM json_each(?) j CROSS JOIN entries e ON e.id = j.value JOIN texts t ON t.id = e.id", []any{idList(ids)}, func(rows *sql.Rows) error {
		var total int
		var score float64
		id, e, err := scanEntry(rows, &total, &score)
		if err != nil {
			return err
		}
		entries[id] = e
		return nil
	})
	if err != nil {
		return page, err
	}
	for _, r := range hits {
		e, ok := entries[r.id]
		if !ok {
			continue
		}
		h := Hit{Entry: e, Links: Links{Out: []LinkEnd{}, In: []LinkEnd{}}}
		if s, ok := snippets[r.id]; ok {
			h.Snippet = clipSnippet(s, MaxSnippet)
		} else if h.Snippet = h.Summary; h.Snippet == "" {
			h.Snippet = Clip(h.Body, MaxSnippet)
		} else {
			h.Snippet = Clip(h.Snippet, MaxSnippet)
		}
		page.Entries = append(page.Entries, h)
		page.after = append(page.after, at(r))
	}
	return page, nil
}
