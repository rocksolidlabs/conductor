package history

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// restrictedTo is a restricted query of line: the chats of one session.
func restrictedTo(t *testing.T, line, session string) Query {
	t.Helper()
	q, err := ParseQuery(line)
	if err != nil {
		t.Fatal(err)
	}
	q.Limit, q.Newest = 10, true
	q.Restrict = &Restriction{Clauses: []Clause{{Kinds: []string{"session.chat"}, Place: &Cond{Column: "anchor", Values: []string{"session:" + session}}}}}
	return q
}

// drawnBy answers q's page and the ids of every row its read drew.
func drawnBy(t *testing.T, ix *Index, q Query) (Page, []int64) {
	t.Helper()
	var drawn []int64
	ix.SetDrawSeam(func(id int64) { drawn = append(drawn, id) })
	defer ix.SetDrawSeam(nil)
	p, err := ix.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return p, drawn
}

// rowsOf is the id of every row of a kind anchored to a ref.
func rowsOf(t *testing.T, ix *Index, kind, anchor string) map[int64]bool {
	t.Helper()
	rows, err := ix.db.Query("SELECT id FROM entries WHERE kind = ? AND anchor = ?", kind, anchor)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id] = true
	}
	return out
}

// A restricted read reads its restriction's rows and no other, in its
// cost too: a session's chats are drawn alone, whatever the line, and
// with ten thousand newer rows outside the restriction that hold the
// line's words (another session's chats, and the session's own rows of
// another kind) the read draws the same rows and answers the same page.
func TestARestrictedReadDrawsOnlyItsRows(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	const own = "aaaaaaaaaaaaaaaa"
	var mine []Entry
	for i := range 5 {
		mine = append(mine, chat(fmt.Sprintf("session:%s#chat/%d", own, i), own, fmt.Sprintf("zebra visible %d", i), t0.Add(time.Duration(i)*time.Minute)))
	}
	put(t, ix, "session:"+own, mine...)
	lines := []string{"zebra", "zeb", "zebr", "visible 3", "", "kind:session.chat"}
	type answer struct {
		keys  []string
		drawn int
	}
	read := func() map[string]answer {
		t.Helper()
		inside := rowsOf(t, ix, "session.chat", "session:"+own)
		out := map[string]answer{}
		for _, line := range lines {
			p, drawn := drawnBy(t, ix, restrictedTo(t, line, own))
			for _, id := range drawn {
				if !inside[id] {
					t.Fatalf("%q: row %d drawn, outside the restriction", line, id)
				}
			}
			if p.Drawn != len(drawn) {
				t.Fatalf("%q: %d drawn, the page says %d", line, len(drawn), p.Drawn)
			}
			out[line] = answer{keysOf(p.Entries), len(drawn)}
		}
		return out
	}
	before := read()
	if got := before["zebra"].keys; len(got) != 5 || got[0] != "session:"+own+"#chat/4" {
		t.Fatalf("the session's chats, newest first: %v", got)
	}
	if got := before["visible 3"].keys; !slices.Equal(got, []string{"session:" + own + "#chat/3"}) {
		t.Fatalf("one chat's words: %v", got)
	}
	for s := range 10 {
		var hidden []Entry
		other := fmt.Sprintf("b%015d", s)
		for i := range 900 {
			hidden = append(hidden, chat(fmt.Sprintf("session:%s#chat/%d", other, i), other, "zebra visible hidden", t0.Add(time.Hour+time.Duration(i)*time.Second)))
		}
		put(t, ix, "session:"+other, hidden...)
	}
	var typed []Entry
	for i := range 1000 {
		e := chat(fmt.Sprintf("session:%s#typed/%d", own, i), own, "zebra visible typed", t0.Add(2*time.Hour+time.Duration(i)*time.Second))
		e.Kind = "session.typed"
		typed = append(typed, e)
	}
	put(t, ix, "session:"+own+"/typed", typed...)
	after := read()
	for _, line := range lines {
		if !slices.Equal(after[line].keys, before[line].keys) || after[line].drawn != before[line].drawn {
			t.Errorf("%q: %v drawing %d, before %v drawing %d", line, after[line].keys, after[line].drawn, before[line].keys, before[line].drawn)
		}
	}
}

// Every statement a restricted read draws by reads one range of its
// index: one kind and one value of its column, its rows in the read's
// order, so the database reads no row the range does not hold and sorts
// nothing.
func TestARestrictedReadDrawsByItsIndex(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	for _, line := range []string{"", "since:2026-01-01 until:2027-01-01"} {
		q, err := ParseQuery(line)
		if err != nil {
			t.Fatal(err)
		}
		q.Newest = true
		planOf(t, ix, q, line != "")
	}
}

// planOf checks the plan of each statement q draws by, with a since or
// none, from the start and from a position.
func planOf(t *testing.T, ix *Index, q Query, since bool) {
	t.Helper()
	for col, pc := range placeColumns {
		c := Clause{Kinds: []string{"session.chat"}}
		if col != "" {
			c.Place = &Cond{Column: col, Values: []string{"v"}}
		}
		q.Restrict = &Restriction{Clauses: []Clause{c}}
		for _, p := range q.places(t0) {
			for _, at := range []bool{false, true} {
				stmt, args := p.stmt, slices.Clone(p.args)
				if at {
					stmt += " AND (at, key) < (?, ?)"
					args = append(args, stamp(t0), "k")
				}
				rows, err := ix.db.Query("EXPLAIN QUERY PLAN "+stmt+" ORDER BY at DESC, key DESC LIMIT ?", append(args, 5)...)
				if err != nil {
					t.Fatalf("%s: %v", col, err)
				}
				var plan []string
				for rows.Next() {
					var id, parent, unused int
					var detail string
					if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
						t.Fatal(err)
					}
					plan = append(plan, detail)
				}
				rows.Close()
				want := "SEARCH entries USING COVERING INDEX " + pc.index + " (kind=?"
				switch {
				case strings.Contains(pc.expr, "("):
					want += " AND <expr>=?"
				case col != "":
					want += " AND " + pc.expr + "=?"
				}
				if since {
					want += " AND at>?"
				}
				if at {
					want += " AND (at,key)<(?,?)"
				}
				if len(plan) != 1 || plan[0] != want+")" {
					t.Errorf("%s, a position %v: the plan %q, want %q", col, at, plan, want+")")
				}
			}
		}
	}
}

// A restricted read draws at most Examine rows, and says so when they
// hold less than a page: its next reads on after the last row it drew,
// and the read, followed on, finds what lies past the bound.
func TestARestrictedReadSaysWhenItsBoundIsSpent(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	const own = "aaaaaaaaaaaaaaaa"
	entries := []Entry{chat("session:"+own+"#chat/old", own, "pelican", t0)}
	for i := range 30 {
		entries = append(entries, chat(fmt.Sprintf("session:%s#chat/%d", own, i), own, "heron", t0.Add(time.Duration(i+1)*time.Minute)))
	}
	put(t, ix, "session:"+own, entries...)
	q := restrictedTo(t, "pelican", own)
	q.Examine = 10
	var found []string
	reads := 0
	for {
		p, drawn := drawnBy(t, ix, q)
		reads++
		if len(drawn) > q.Examine {
			t.Fatalf("read %d drew %d rows, more than %d", reads, len(drawn), q.Examine)
		}
		found = append(found, keysOf(p.Entries)...)
		if len(found) == 0 && (p.Cut != CutExamined || p.Next == "") {
			t.Fatalf("read %d, short of a page at its bound: cut %q, next %q", reads, p.Cut, p.Next)
		}
		if p.Next == "" {
			break
		}
		q.After = p.Next
	}
	if !slices.Equal(found, []string{"session:" + own + "#chat/old"}) || reads < 4 {
		t.Fatalf("found %v in %d reads", found, reads)
	}
	q = restrictedTo(t, "pelican", own)
	q.Examine = MaxExamine + 1
	if _, err := ix.Search(context.Background(), q); err == nil {
		t.Fatal("a read past MaxExamine")
	}
	q = restrictedTo(t, "pelican", own)
	q.Restrict = nil
	if _, err := ix.Search(context.Background(), q); err == nil {
		t.Fatal("a read newest first with no restriction")
	}
}
