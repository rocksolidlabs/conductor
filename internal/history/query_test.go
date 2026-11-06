package history

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Every term must match, each in any of the title, the summary and the
// body: not necessarily the same column.
func TestTermsIntersectAcrossColumns(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	both := Entry{Key: "k1", Kind: "message", Ref: "message:r/1", Anchor: "run:r", Title: "deploy pipeline", Summary: "from builder", Body: "a timeout in the stage", At: t0}
	one := Entry{Key: "k2", Kind: "message", Ref: "message:r/2", Anchor: "run:r", Title: "deploy", Body: "nothing else", At: t0}
	put(t, ix, "run:r", both, one)
	wantKeys(t, ix, "deploy timeout", "k1")
	wantKeys(t, ix, "timeout DEPLOY", "k1")
	wantKeys(t, ix, "builder timeout pipeline", "k1")
	wantSameKeys(t, ix, "deploy", "k1", "k2")
	wantKeys(t, ix, "deploy absent")
}

// The last word is a prefix at any length, from one character; an earlier
// word is whole.
func TestTheLastWordIsAPrefix(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	put(t, ix, "session:s1", chat("e1", "s1", "login redirect", t0), chat("e2", "s1", "logout", t0))
	wantSameKeys(t, ix, "l", "e1", "e2")
	wantSameKeys(t, ix, "lo", "e1", "e2")
	wantSameKeys(t, ix, "log", "e1", "e2")
	wantKeys(t, ix, "logi", "e1")
	wantKeys(t, ix, "logi redirect")
	wantKeys(t, ix, "redirect logi", "e1")
	wantKeys(t, ix, "r", "e1")
	q, _ := ParseQuery("Login login")
	if !slices.Equal(q.Terms(), []string{`"Login"`}) {
		t.Errorf("a word repeated: %q", q.Terms())
	}
}

// A word with no letter or digit is dropped: what is left is searched,
// and a line of nothing else matches nothing (and is no error).
func TestAWordWithNoLetterOrDigitIsDropped(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	failed := receipt("check:r/1", "r", "sub", "failed", "login failed", t0)
	put(t, ix, "session:s1", chat("e1", "s1", "login -- works", t0))
	put(t, ix, "check:r/1", failed)
	wantSameKeys(t, ix, "login --", "e1", "check:r/1")
	wantSameKeys(t, ix, "-- login", "e1", "check:r/1")
	wantKeys(t, ix, "-- ... ***")
	wantKeys(t, ix, `""`)
	wantKeys(t, ix, "-- outcome:failed", "check:r/1")
	q, _ := ParseQuery(`OR NOT a:b CND-12 (x) "`)
	want := []string{`"OR"`, `"NOT"`, `"a:b"`, `"CND-12"`, `"(x)"*`}
	if !slices.Equal(q.Terms(), want) {
		t.Errorf("words as words: %q, want %q", q.Terms(), want)
	}
}

// A quoted phrase is one term, its words in order.
func TestAPhraseIsOneTerm(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	put(t, ix, "session:s1", chat("e1", "s1", "--- FAIL: TestLoginRedirect: the login redirect failed", t0), chat("e2", "s1", "redirect, then login", t0))
	wantKeys(t, ix, `"login redirect"`, "e1")
	wantSameKeys(t, ix, "login redirect", "e1", "e2")
	wantKeys(t, ix, `"FAIL: TestLoginRedirect"`, "e1")
	wantKeys(t, ix, `"redirect then login`, "e2") // a quote left open runs to the end
	wantKeys(t, ix, `"login then"`)
	q, _ := ParseQuery(`fail "login  redirect" x`)
	if !slices.Equal(q.Terms(), []string{`"fail"`, `"x"*`, `"login redirect"`}) {
		t.Errorf("terms %q", q.Terms())
	}
}

// Each filter, over two entries that differ in every field it reads, and
// what each refuses.
func TestEachFilter(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	a := Entry{Key: "a", Kind: "session.chat", Ref: "session:0123456789abcdef", Anchor: "session:0123456789abcdef", Title: "alpha",
		Project: "CND", Repo: "/home/n/code/todo/.git", Host: "lab-box", Agent: "claude", Crew: "team", Run: "team-1", Ticket: "CND-12",
		At: t0.Add(-48 * time.Hour)}
	b := receipt("b", "review-0a1b2c3d", "sub", "failed", "alpha", t0.Add(-time.Hour))
	b.Project, b.Repo, b.Host, b.Agent, b.Crew, b.Ticket = "ZED", "/home/n/code/other/.git", "mac", "codex", "review", "ZED-3"
	// The longest crew id internal/crew takes (ValidID) and a run of it
	// (ValidRunID: the crew's id, a dash, eight hex digits; 73 bytes).
	longCrew := "c" + strings.Repeat("rew-", 15) + "abc"
	c := chat("c", "sc", "gamma", t0.Add(-72*time.Hour))
	c.Crew, c.Run = longCrew, longCrew+"-0123abcd"
	put(t, ix, "s", a, b, c)
	if len(longCrew) != 64 || len(c.Run) != 73 {
		t.Fatalf("the crew's id is %d bytes and its run's %d", len(longCrew), len(c.Run))
	}
	for _, c := range []struct {
		line string
		want []string
	}{
		{"class:said", []string{"a", "c"}}, {"class:conversation", []string{"a", "c"}}, {"class:checked", []string{"b"}},
		{"class:said,checked", []string{"a", "b", "c"}}, {"class:decided", nil}, {"class:record", nil}, {"CLASS:Decision", nil},
		{"kind:check", []string{"b"}}, {"kind:session.chat,check", []string{"a", "b", "c"}},
		{"outcome:failed", []string{"b"}}, {"outcome:passed", nil},
		{"project:cnd", []string{"a"}}, {"project:CND,ZED", []string{"a", "b"}},
		{"repo:todo", []string{"a"}}, {"repo:code", []string{"a", "b"}}, {"repo:code/other", []string{"b"}}, {"repo:/home/n/code/todo/", []string{"a"}},
		{"repo:od", nil}, {"repo:.git", []string{"a", "b"}},
		{"host:LAB-BOX", []string{"a"}}, {"host:mac", []string{"b"}},
		{"agent:codex", []string{"b"}}, {"crew:team", []string{"a"}}, {"run:review-0a1b2c3d", []string{"b"}},
		{"crew:" + longCrew, []string{"c"}}, {"run:" + longCrew + "-0123abcd", []string{"c"}},
		{"ticket:cnd-12", []string{"a"}}, {"session:0123456789ABCDEF", []string{"a"}},
		{"since:24h", []string{"b"}}, {"until:24h", []string{"a", "c"}}, {"since:1w", []string{"a", "b", "c"}},
		{"since:2026-11-04", []string{"b"}}, {"until:2026-11-04", []string{"a", "c"}}, {"until:2026-11-05", []string{"a", "b", "c"}},
		{"since:2026-11-05T10:30:00Z", []string{"b"}}, {"since:2026-11-05T11:30:00Z", nil},
		// The longest spans a time.Duration holds, and the largest counts
		// of hours and days: every entry is newer than each.
		{"since:15250w", []string{"a", "b", "c"}}, {"since:99999d", []string{"a", "b", "c"}}, {"since:99999h", []string{"a", "b", "c"}},
		{"until:15250w", nil},
		{"alpha class:checked outcome:failed", []string{"b"}}, {"class:said class:checked", nil},
	} {
		wantSameKeys(t, ix, c.line, c.want...)
	}
	for _, line := range []string{
		"class:bogus", "kind:nope", "outcome:Bad!", "project:1X", "project:TOOLONGKEY", "repo:/", "agent:Bad_Agent!", "crew:-x",
		"crew:" + longCrew + "x", "crew:a_b", "run:-x", "run:team-1", "run:review-0a1b2c3", "run:" + longCrew + "x-0123abcd", "ticket:CND-0", "ticket:CND", "session:xyz", "since:yesterday", "until:7y", "since:0d", "since:1d since:2d",
		// A week past the longest span, and spans whose product wraps
		// (30501w wraps to some three days, 99999w past it).
		"since:15251w", "since:30501w", "until:99999w", "since:100000h",
		"class:", "class:said,", "outcome:" + strings.Repeat("a,", MaxFilterValues) + "a",
		strings.Repeat("x", MaxQuery+1), manyWords(MaxTerms + 1), "\xff",
	} {
		if _, err := ParseQuery(line); !errors.Is(err, ErrQuery) {
			t.Errorf("%q: %v, want a refusal", line, err)
		} else if strings.TrimPrefix(err.Error(), ErrQuery.Error()+": ") == "" {
			t.Errorf("%q: a refusal with no words", line)
		}
	}
}

// A name the filters do not take is a word, the whole of it.
func TestAnUnknownFilterIsAWord(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	put(t, ix, "s", chat("e1", "s", "see http://x for it", t0), chat("e2", "s", "foo bar", t0), chat("e3", "s", "nothing", t0))
	wantKeys(t, ix, "http://x", "e1")
	wantKeys(t, ix, "foo:bar", "e2")
	q, _ := ParseQuery("http://x")
	if !slices.Equal(q.Terms(), []string{`"http://x"*`}) || len(q.filters) != 0 {
		t.Errorf("terms %q filters %v", q.Terms(), q.filters)
	}
}

// Two entries of the same text, a said one newer than a checked one: the
// newer first, with words and with filters alone; and the other way about
// when the checked one is newer. Class never weighs in the order.
func TestOrderNeverWeighsClass(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	words := receipt("check:r/1", "r", "sub", "passed", "", t0.Add(-time.Hour))
	words.Title, words.Summary, words.Body = "alpha", "", "beta"
	saidOne := chat("e1", "s", "beta", t0)
	saidOne.Title = "alpha"
	put(t, ix, "check:r/1", words)
	put(t, ix, "session:s", saidOne)
	wantKeys(t, ix, "alpha beta", "e1", "check:r/1")
	wantKeys(t, ix, "class:said,checked", "e1", "check:r/1")
	words.At = t0.Add(time.Hour)
	put(t, ix, "check:r/1", words)
	wantKeys(t, ix, "alpha beta", "check:r/1", "e1")
	wantKeys(t, ix, "class:said,checked", "check:r/1", "e1")
}

// 250 entries of one text, one score and one time, paged by 20 and by
// 100, with words and with filters alone: each key exactly once, in key
// order. A cursor of another query is refused, one that does not decode
// too, and one taken before a reindex is stale.
func TestTiedEntriesPageExactlyOnce(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	var es []Entry
	var want []string
	for i := range 250 {
		k := fmt.Sprintf("session:s#e%03d", i)
		es = append(es, chat(k, "s", "tied words", t0))
		want = append(want, k)
	}
	put(t, ix, "session:s", es...)
	for _, line := range []string{"tied", "class:said"} {
		for _, limit := range []int{20, 100} {
			q, err := ParseQuery(line)
			if err != nil {
				t.Fatal(err)
			}
			q.Limit = limit
			var got []string
			for pages := 0; ; pages++ {
				p, err := ix.Search(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				if p.Total != 250 {
					t.Fatalf("%q: total %d", line, p.Total)
				}
				got = append(got, keysOf(p.Entries)...)
				if p.Next == "" {
					break
				}
				if pages > 20 {
					t.Fatal("no end of pages")
				}
				q.After = p.Next
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%q by %d: %d keys, not each once in key order", line, limit, len(got))
			}
		}
	}
	first := search(t, ix, "tied", 20)
	other, _ := ParseQuery("tied words")
	other.After = first.Next
	_, err := ix.Search(ctx, other)
	wantErr(t, err, ErrCursor)
	for _, bad := range []string{"%%%", "e30", strings.Repeat("A", maxCursor+1)} {
		q, _ := ParseQuery("tied")
		q.After = bad
		_, err := ix.Search(ctx, q)
		wantErr(t, err, ErrCursor)
	}
	if err := ix.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	put(t, ix, "session:s", es...)
	q, _ := ParseQuery("tied")
	q.After = first.Next
	_, err = ix.Search(ctx, q)
	wantErr(t, err, ErrStaleCursor)
}

// A page counts its search to MaxTotal and says when it stopped there.
func TestTheTotalIsCapped(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	es := make([]Entry, MaxTotal+1)
	for i := range es {
		es[i] = chat(fmt.Sprintf("e%05d", i), "s", "many", t0)
	}
	put(t, ix, "s", es...)
	if p := search(t, ix, "many", 0); p.Total != MaxTotal || !p.TotalCapped || len(p.Entries) != DefaultLimit {
		t.Fatalf("total %d capped %v entries %d", p.Total, p.TotalCapped, len(p.Entries))
	}
	if p := search(t, ix, "", 0); p.Total != MaxTotal || !p.TotalCapped {
		t.Fatalf("no words: total %d capped %v", p.Total, p.TotalCapped)
	}
}

// A hit's snippet is drawn from its best column with the match marked, at
// most 300 bytes; a hit of filters alone shows its summary.
func TestSnippetMarksTheMatch(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	log := strings.Repeat("ok  pkg/other 0.1s\n", 100) + "--- FAIL: TestLoginRedirect (0.02s)\n" + strings.Repeat("more output here\n", 100)
	put(t, ix, "check:r/1", receipt("check:r/1", "r", "sub", "failed", log, t0))
	for _, line := range []string{"TestLoginRedirect", "testloginre"} {
		p := search(t, ix, line, 0)
		if len(p.Entries) != 1 {
			t.Fatalf("%q: %d entries", line, len(p.Entries))
		}
		s := p.Entries[0].Snippet
		if !strings.Contains(s, "[TestLoginRedirect]") || len(s) > MaxSnippet {
			t.Errorf("%q: the snippet %q (%d bytes)", line, s, len(s))
		}
	}
	if s := search(t, ix, "kind:check", 0).Entries[0].Snippet; s != "go test ./..." {
		t.Errorf("no words: the snippet %q", s)
	}
	// Long tokens before the match (40 of 40 letters, the match after the
	// 21st: snippet() bounds its window in tokens, and a cut from the
	// start would lose the match), a CJK sentence the tokenizer reads as
	// one token, and text that holds brackets of its own: the window is
	// cut around the first match, its mark whole, … where it was cut.
	var long []string
	for i := range 40 {
		long = append(long, strings.Repeat(string(rune('a'+i%26)), 40))
		if i == 20 {
			long = append(long, "needle")
		}
	}
	cjk := strings.Repeat("検索の結果は", 30) + " needle " + strings.Repeat("ここにある", 30)
	brackets := strings.Repeat("[x] ", 60) + "needle " + strings.Repeat("[y] ", 60)
	for i, body := range []string{strings.Join(long, " "), cjk, brackets} {
		ix := openIndex(t, t.TempDir(), Options{})
		put(t, ix, "session:s", chat("session:s#e1", "s", body, t0))
		s := search(t, ix, "needle", 0).Entries[0].Snippet
		if !strings.Contains(s, "[needle]") || len(s) > MaxSnippet || !utf8.ValidString(s) || !strings.HasPrefix(s, "…") {
			t.Errorf("body %d: the snippet %q (%d bytes)", i, s, len(s))
		}
	}
	// Text that holds the snippet's own marks (U+E000, U+E001) beside the
	// match: they are indexed as U+FFFD, so the snippet's only marks are
	// the search's.
	own := strings.Repeat(snipClose+"x"+snipOpen+" ", 40) + "needle " + strings.Repeat(snipOpen+"y ", 40)
	ix = openIndex(t, t.TempDir(), Options{})
	put(t, ix, "session:s", chat("session:s#e1", "s", own, t0))
	if s := search(t, ix, "needle", 0).Entries[0].Snippet; strings.Count(s, "[") != 1 || strings.Count(s, "]") != 1 ||
		!strings.Contains(s, "[needle]") || strings.ContainsAny(s, snipOpen+snipClose) || len(s) > MaxSnippet {
		t.Errorf("a text holding the marks: the snippet %q (%d bytes)", s, len(s))
	}
	if e, err := ix.Entry(context.Background(), "session:s#e1"); err != nil || strings.ContainsAny(e.Body, snipOpen+snipClose) {
		t.Errorf("a text holding the marks is indexed with them: %v", err)
	}
}

// clipSnippet keeps both marks of every match it shows, whole, however
// it cuts: a match at the start (cut at the end only), at the end (cut at
// the start only), in the middle (cut at both), a match longer than the
// bound at each place, a second match the window ends in, and text that
// holds brackets of its own. Each is at most the bound, valid UTF-8,
// starts with … exactly when its start was cut and ends with … exactly
// when its end was.
func TestClipSnippetKeepsItsMarks(t *testing.T) {
	m := func(s string) string { return snipOpen + s + snipClose }
	w := strings.Repeat("word ", 100)
	long := strings.Repeat("m", 400)
	cjk := strings.Repeat("検索", 100)
	for _, c := range []struct {
		name, in   string
		head, tail bool // … at the start, at the end
		match      string
	}{
		{"at the start", m("hit") + " " + w, false, true, "[hit]"},
		{"at the end", w + m("hit"), true, false, "[hit]"},
		{"in the middle", w + m("hit") + " " + w, true, true, "[hit]"},
		{"a second match cut", w + m("hit") + " " + w + m("two") + w, true, true, "[hit]"},
		{"long at the start", m(long) + " " + w, false, true, "[mmm"},
		{"long at the end", w + m(long), true, false, "[mmm"},
		{"long in the middle", w + m(long) + w, true, true, "[mmm"},
		{"long and alone", m(long), false, false, "[mmm"},
		{"long CJK", w + m(cjk) + w, true, true, "[検索"},
		{"its own brackets", strings.Repeat("[x] ", 60) + m("hit") + strings.Repeat(" [y]", 60), true, true, "[hit]"},
		{"short", "a " + m("hit") + " b", false, false, "a [hit] b"},
	} {
		s := clipSnippet(c.in, MaxSnippet)
		if len(s) > MaxSnippet || !utf8.ValidString(s) || strings.ContainsAny(s, snipOpen+snipClose) || !strings.Contains(s, c.match) {
			t.Errorf("%s: %q (%d bytes)", c.name, s, len(s))
		}
		if strings.HasPrefix(s, "…") != c.head || strings.HasSuffix(s, "…") != c.tail {
			t.Errorf("%s: cut at the start %v, at the end %v, want %v, %v: %q", c.name, strings.HasPrefix(s, "…"), strings.HasSuffix(s, "…"), c.head, c.tail, s)
		}
		// The marks, read from the marked text the window was cut from:
		// none open at the end, none closed before it opened.
		if c.name == "its own brackets" {
			continue
		}
		depth := 0
		for _, r := range s {
			switch r {
			case '[':
				depth++
			case ']':
				depth--
			}
			if depth < 0 || depth > 1 {
				break
			}
		}
		if depth != 0 || strings.Count(s, "[") != strings.Count(s, "]") {
			t.Errorf("%s: a mark cut through: %q", c.name, s)
		}
	}
}

// A link's far end shows the entry that stands for its ref: the entry
// keyed by it, else the first by key of those whose ref it is (a
// command's key is not its ref), never none when the index holds one.
func TestALinkFindsItsEndByItsReference(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	const run = "team-0a1b2c3d"
	cmd := Entry{Key: "run:" + run + "#command/admin/c1", Kind: "command", Ref: "command:" + run + "/c1", Anchor: "run:" + run,
		Actor: "admin", Title: "retry", At: t0}
	put(t, ix, "run:"+run, cmd)
	item := Entry{Key: "brief:br-1#i1", Kind: "brief.item", Ref: "brief:br-1", Anchor: "brief:br-1", Title: "Brakeman's fact", Body: "it was retried", At: t0}
	links := []Link{{From: "brief:br-1", Rel: "cites", To: cmd.Ref}, {From: "brief:br-1", Rel: "cites", To: "session:s"}}
	if err := ix.Replace(ctx, Source{Key: "brief:br-1", Mark: "m"}, []Entry{item}, links); err != nil {
		t.Fatal(err)
	}
	far := func() map[string]string {
		t.Helper()
		l, err := ix.Links(ctx, "brief:br-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, end := range l.Out {
			out[end.Ref] = ""
			if end.Entry != nil {
				out[end.Ref] = end.Entry.Key
			}
		}
		return out
	}
	if got := far(); got[cmd.Ref] != cmd.Key || got["session:s"] != "" {
		t.Fatalf("the ends: %v", got)
	}
	// Two lines of a session carry its ref: the first by key stands for
	// it, until the index holds the session's own record.
	put(t, ix, "session:s", chat("session:s#e2", "s", "two", t0), chat("session:s#e1", "s", "one", t0))
	if got := far(); got["session:s"] != "session:s#e1" {
		t.Fatalf("a shared ref: %v", got)
	}
	record := Entry{Key: "session:s", Kind: "session", Ref: "session:s", Anchor: "session:s", Title: "the session", At: t0}
	put(t, ix, "session:s", chat("session:s#e2", "s", "two", t0), chat("session:s#e1", "s", "one", t0), record)
	if got := far(); got["session:s"] != "session:s" {
		t.Fatalf("a ref with its record: %v", got)
	}
	if h, err := ix.Entry(ctx, item.Key); err != nil || len(h.Links.Out) != 2 {
		t.Fatalf("the entry's links: %+v %v", h.Links, err)
	}
}

// A search of words past its bounds says so and answers no wrong row:
// past maxRanked passing matches it ranks the most recently indexed
// maxRanked (the best scored first) and counts every one; past
// maxScanned matches it reads the most recently indexed maxScanned, newest
// first, and counts every match (with filters, those it read that pass
// them); under both it reads and ranks every match.
func TestASearchPastItsBoundsSaysSo(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	ix.maxRanked, ix.maxScanned = 3, 6
	// e1 indexed first, e7 last; each an hour older than the one before.
	add := func(i int) {
		e := chat(fmt.Sprintf("session:s%d#e1", i), fmt.Sprint(i), "alpha", t0.Add(-time.Duration(i)*time.Hour))
		if i == 1 || i == 4 {
			e.Title = "alpha" // scored first
		}
		if i == 6 {
			e.Kind, e.Ref, e.Commit = "check", e.Key, "0123456789abcdef0123456789abcdef01234567"
		}
		put(t, ix, fmt.Sprintf("session:s%d", i), e)
	}
	for i := 1; i <= 5; i++ {
		add(i)
	}
	put(t, ix, "session:rare", chat("session:rare#e1", "rare", "omega", t0))
	if p := search(t, ix, "omega", 0); p.Cut != "" || p.Total != 1 {
		t.Fatalf("under the bounds: cut %q total %d", p.Cut, p.Total)
	}
	// Five match, three ranked: e3, e4, e5, e4 first by its score; e1, the
	// best scored, is past the bound.
	p := search(t, ix, "alpha", 0)
	if p.Cut != CutRanked || p.Total != 5 || !slices.Equal(keysOf(p.Entries), []string{"session:s4#e1", "session:s3#e1", "session:s5#e1"}) {
		t.Fatalf("past maxRanked: cut %q total %d %v", p.Cut, p.Total, keysOf(p.Entries))
	}
	if p := search(t, ix, "alpha class:said", 0); p.Cut != CutRanked || p.Total != 5 {
		t.Fatalf("past maxRanked, filtered: cut %q total %d", p.Cut, p.Total)
	}
	add(6)
	add(7)
	// Seven match: the six most recently indexed (e2 to e7) read, newest
	// first, unscored; paged by two, each once.
	window := []string{"session:s2#e1", "session:s3#e1", "session:s4#e1", "session:s5#e1", "session:s6#e1", "session:s7#e1"}
	if p := search(t, ix, "alpha", 0); p.Cut != CutScanned || p.Total != 7 || !slices.Equal(keysOf(p.Entries), window) {
		t.Fatalf("past maxScanned: cut %q total %d %v", p.Cut, p.Total, keysOf(p.Entries))
	}
	q, _ := ParseQuery("alpha")
	q.Limit = 2
	var got []string
	for {
		p, err := ix.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if p.Cut != CutScanned {
			t.Fatalf("a page past maxScanned: cut %q", p.Cut)
		}
		got = append(got, keysOf(p.Entries)...)
		if p.Next == "" {
			break
		}
		q.After = p.Next
	}
	if !slices.Equal(got, window) {
		t.Fatalf("paged past maxScanned: %v", got)
	}
	// Filtered: the window's five said (e6 is a receipt), counted as read.
	if p := search(t, ix, "alpha class:said", 0); p.Cut != CutScanned || p.Total != 5 || len(p.Entries) != 5 {
		t.Fatalf("past maxScanned, filtered: cut %q total %d %v", p.Cut, p.Total, keysOf(p.Entries))
	}
	if p := search(t, ix, "alpha class:checked", 0); p.Cut != CutScanned || p.Total != 1 || p.Entries[0].Key != "session:s6#e1" {
		t.Fatalf("past maxScanned, one passing: cut %q total %d %v", p.Cut, p.Total, keysOf(p.Entries))
	}
}

// Similar finds the receipts of the same command in other runs of the
// repository, the entry's own run chain left out, and none of another
// command that does not share the failure's line.
func TestSimilarFindsTheSameCommandElsewhereAndLeavesTheChainOut(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	const repo = "/home/n/code/todo/.git"
	r := func(key, run, subject, outcome string, h int) Entry {
		e := receipt(key, run, subject, outcome, "some output", t0.Add(time.Duration(h)*time.Hour))
		e.Repo = repo
		return e
	}
	failure := r("check:r1/build/1/unit/1", "r1", "S", "failed", 0)
	failure.Body = "lots of output\nFAIL: TestLoginRedirect\n"
	put(t, ix, "src",
		failure,
		r("check:r1/build/2/unit/1", "r1", "S", "passed", 1),   // the same run
		r("check:r1b/build/1/unit/1", "r1b", "S", "passed", 2), // its continuation
		r("check:r2/build/1/unit/1", "r2", "S", "passed", 3),
		r("check:r3/build/1/unit/1", "r3", "S", "failed", 4),
		r("check:r4/build/1/unit/1", "r4", "T", "passed", 5),     // another command, without the line
		chat("session:s#e1", "s", "FAIL: TestLoginRedirect", t0), // not a receipt
	)
	hits, err := ix.Similar(ctx, failure.Key, ScopeRepo, []string{"r1b"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(hits); !slices.Equal(got, []string{"check:r2/build/1/unit/1", "check:r3/build/1/unit/1"}) {
		t.Fatalf("similar: %v", got)
	}
	if hits, err := ix.Similar(ctx, "session:s#e1", ScopeRepo, nil, 0); err != nil || len(hits) != 0 {
		t.Fatalf("of a chat line: %v %v", keysOf(hits), err)
	}
	_, err = ix.Similar(ctx, "check:none", ScopeRepo, nil, 0)
	wantErr(t, err, ErrGone)
	_, err = ix.Similar(ctx, failure.Key, "everywhere", nil, 0)
	wantErr(t, err, ErrQuery)
}

// Two repositories whose crews run one generic argv: in the failure's
// repository the receipts that hold its line rank first, passed before
// failed, then the rest of the same command's, then another command's
// that holds the line; the other repository's are absent until the scope
// is all. A project, when the entry has one, is the repository's scope.
func TestSimilarStaysInTheRepositoryAndRanksTheLineFirst(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	const line = "--- FAIL: TestLoginRedirect (0.02s)"
	r := func(key, repo, subject, outcome, body string, h int) Entry {
		e := receipt(key, "run-"+key, subject, outcome, body, t0.Add(time.Duration(h)*time.Hour))
		e.Repo = repo
		return e
	}
	const a, b = "/code/a/.git", "/code/b/.git"
	failure := r("f", a, "G", "failed", "ok pkg 0.1s\n"+line+"\nFAIL\tpkg/login\n", 0)
	failure.Body = "ok pkg 0.1s\n" + line + "\n"
	put(t, ix, "src",
		failure,
		r("a-passed-line", a, "G", "passed", "a passing run that printed "+line+" then", 1),
		r("a-failed-line", a, "G", "failed", "x\n"+line+"\n", 2),
		r("a-failed-line-newer", a, "G", "failed", line, 3),
		r("a-passed", a, "G", "passed", "PASS", 4),
		r("a-failed", a, "G", "failed", "FAIL: TestOther", 5),
		r("a-other-command-line", a, "H", "failed", line, 6),
		r("a-other-command", a, "H", "failed", "FAIL: TestOther", 7),
		r("b-passed", b, "G", "passed", "PASS", 8),
		r("b-failed-line", b, "G", "failed", line, 9),
	)
	hits, err := ix.Similar(ctx, "f", ScopeRepo, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a-passed-line", "a-failed-line-newer", "a-failed-line", "a-passed", "a-failed", "a-other-command-line"}
	if got := keysOf(hits); !slices.Equal(got, want) {
		t.Fatalf("in the repository: %v\nwant %v", got, want)
	}
	hits, err = ix.Similar(ctx, "f", ScopeAll, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"a-passed-line", "b-failed-line", "a-failed-line-newer", "a-failed-line", "b-passed", "a-passed", "a-failed", "a-other-command-line"}
	if got := keysOf(hits); !slices.Equal(got, want) {
		t.Fatalf("everywhere: %v\nwant %v", got, want)
	}
	if hits, _ := ix.Similar(ctx, "f", ScopeAll, nil, 2); len(hits) != 2 {
		t.Fatalf("a limit of 2: %d", len(hits))
	}
	// With a project, the project is the scope: a sibling worktree's
	// receipt of the same project counts, another project's does not.
	failure.Project = "CND"
	sibling := r("sibling", "/code/a-sibling/.git", "G", "passed", "PASS", 10)
	sibling.Project = "CND"
	other := r("other-project", a, "G", "passed", "PASS", 11)
	other.Project = "ZED"
	put(t, ix, "src2", sibling, other)
	put(t, ix, "src", failure)
	hits, _ = ix.Similar(ctx, "f", ScopeRepo, nil, 0)
	if got := keysOf(hits); !slices.Equal(got, []string{"sibling"}) {
		t.Fatalf("by project: %v", got)
	}
}

// manyWords is n distinct words.
func manyWords(n int) string {
	var ws []string
	for i := range n {
		ws = append(ws, fmt.Sprintf("w%d", i))
	}
	return strings.Join(ws, " ")
}

// ExceptCrews leaves a crew's entries out of a search, of words and of
// filters alone, and its cursor is another query's; Filter answers what
// the line gave a filter.
func TestExceptCrewsLeavesTheirEntriesOut(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	mine := chat("session:a#e1", "a", "the turnstile jammed", t0)
	turn := chat("session:b#e1", "b", "the turnstile jammed again", t0.Add(time.Minute))
	turn.Crew = "brakeman"
	put(t, ix, "session:a", mine)
	put(t, ix, "session:b", turn)
	for _, line := range []string{"turnstile", "class:said"} {
		q, err := ParseQuery(line)
		if err != nil {
			t.Fatal(err)
		}
		all, err := ix.Search(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		q.ExceptCrews = []string{"brakeman"}
		some, err := ix.Search(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(some.Entries); len(all.Entries) != 2 || !slices.Equal(got, []string{"session:a#e1"}) || some.Total != 1 {
			t.Fatalf("%q: all %v, except brakeman %v (total %d)", line, keysOf(all.Entries), got, some.Total)
		}
	}
	q, _ := ParseQuery("turnstile")
	q.Limit = 1
	first, err := ix.Search(t.Context(), q)
	if err != nil || first.Next == "" {
		t.Fatalf("a first page of one: %v %+v", err, first)
	}
	q.After, q.ExceptCrews = first.Next, []string{"brakeman"}
	if _, err := ix.Search(t.Context(), q); !errors.Is(err, ErrCursor) {
		t.Fatalf("a cursor of the search without the exception: %v", err)
	}
	q, _ = ParseQuery("crew:brakeman,ops kind:brief.item turnstile")
	if v, ok := q.Filter("crew"); !ok || !slices.Equal(v, []string{"brakeman", "ops"}) {
		t.Fatalf("crew: %v %v", v, ok)
	}
	if v, ok := q.Filter("kind"); !ok || !slices.Equal(v, []string{"brief.item"}) {
		t.Fatalf("kind: %v %v", v, ok)
	}
	if _, ok := q.Filter("project"); ok {
		t.Fatal("project: given")
	}
}

// ExceptSources leaves a source's entries out of a search and out of its
// total, of words and of filters alone; a cursor read with one set reads
// on with another, for it moves no row.
func TestExceptSourcesLeavesTheirEntriesOut(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	put(t, ix, "session:a", chat("session:a#e1", "a", "the turnstile jammed", t0))
	put(t, ix, "session:b", chat("session:b#e1", "b", "the turnstile jammed again", t0.Add(time.Minute)))
	put(t, ix, "session:c", chat("session:c#e1", "c", "the turnstile jammed at last", t0.Add(2*time.Minute)))
	for _, line := range []string{"turnstile", "class:said"} {
		q, err := ParseQuery(line)
		if err != nil {
			t.Fatal(err)
		}
		q.ExceptSources = []string{"session:b"}
		some, err := ix.Search(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(some.Entries); len(got) != 2 || slices.Contains(got, "session:b#e1") || some.Total != 2 {
			t.Fatalf("%q except session:b: %v (total %d)", line, got, some.Total)
		}
	}
	q, _ := ParseQuery("class:said")
	q.Limit = 1
	first, err := ix.Search(t.Context(), q)
	if err != nil || first.Next == "" || !slices.Equal(keysOf(first.Entries), []string{"session:c#e1"}) {
		t.Fatalf("a first page of one: %v %+v", err, first)
	}
	q.After, q.ExceptSources = first.Next, []string{"session:b"}
	next, err := ix.Search(t.Context(), q)
	if err != nil || !slices.Equal(keysOf(next.Entries), []string{"session:a#e1"}) {
		t.Fatalf("the next page except session:b: %v %v", err, keysOf(next.Entries))
	}
}

// The sources a search leaves out (ExceptSources, and Entry's, Links',
// Similar's and Stats' except) count in nothing it answers: of a search,
// on every page, the rows, the total, the cursors, each row's repeats and
// links; of an entry, its repeats and links; Similar's answers; Stats'
// oldest. Each answer with them left out is the answer once they are
// removed, with sources left out older and newer than the kept ones.
func TestExceptSourcesCountInNothing(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	gone := []string{"session:old", "session:new"}
	for i, id := range []string{"old", "a", "b", "new"} {
		at := t0.Add(time.Duration(i) * time.Minute)
		said := chat("session:"+id+"#e1", id, "the turnstile jammed", at)
		check := receipt("session:"+id+"#check", "run-"+id, "sha-go-test", "failed", "FAIL TestTurnstile", at)
		check.Ref = "check:run-" + id + "/build/1/unit/1"
		links := []Link{{From: "run:r1", Rel: "member", To: "session:" + id}}
		if id == "a" {
			links = append(links, Link{From: "session:a", Rel: "resumed_from", To: "session:old"})
		}
		if err := ix.Replace(t.Context(), Source{Key: "session:" + id, Mark: "m1"}, []Entry{said, check}, links); err != nil {
			t.Fatal(err)
		}
	}
	type answer struct {
		Pages   []Page
		Entry   Hit
		Links   Links
		Similar []Hit
		Oldest  time.Time
	}
	read := func(line string, except []string) answer {
		t.Helper()
		var a answer
		q, err := ParseQuery(line)
		if err != nil {
			t.Fatal(err)
		}
		q.Limit, q.ExceptSources = 1, except
		for {
			p, err := ix.Search(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			p.after = nil
			a.Pages = append(a.Pages, p)
			if p.Next == "" {
				break
			}
			q.After = p.Next
		}
		if a.Entry, err = ix.Entry(t.Context(), "session:a#e1", except...); err != nil {
			t.Fatal(err)
		}
		if a.Links, err = ix.Links(t.Context(), "run:r1", MaxLinks, except...); err != nil {
			t.Fatal(err)
		}
		if a.Similar, err = ix.Similar(t.Context(), "session:a#check", ScopeAll, nil, MaxSimilar, except...); err != nil {
			t.Fatal(err)
		}
		st, err := ix.Stats(t.Context(), except...)
		if err != nil {
			t.Fatal(err)
		}
		a.Oldest = st.Oldest
		return a
	}
	lines := []string{"class:said", "turnstile", "kind:check"}
	left := map[string]answer{}
	for _, line := range lines {
		left[line] = read(line, gone)
	}
	for _, key := range []string{"session:old#e1", "session:new#check"} {
		if _, err := ix.Entry(t.Context(), key, gone...); !errors.Is(err, ErrGone) {
			t.Fatalf("an entry of a source left out, %s: %v", key, err)
		}
		if _, err := ix.Similar(t.Context(), key, ScopeAll, nil, MaxSimilar, gone...); !errors.Is(err, ErrGone) {
			t.Fatalf("Similar of a source left out, %s: %v", key, err)
		}
	}
	for _, src := range gone {
		if err := ix.Remove(t.Context(), src); err != nil {
			t.Fatal(err)
		}
	}
	for _, line := range lines {
		got, want := left[line], read(line, nil)
		if line == "turnstile" {
			// A score is the corpus's: the cursors of a search of words
			// move once rows are removed, the rows themselves do not.
			for _, ps := range [][]Page{got.Pages, want.Pages} {
				for i := range ps {
					ps[i].Next = ""
				}
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q with the sources left out:\n%+v\nonce they are removed:\n%+v", line, got, want)
		}
		if n := len(got.Pages); n != 2 || got.Pages[0].Total != 2 {
			t.Errorf("%q: %d pages, total %d: the two kept rows, one a page", line, n, got.Pages[0].Total)
		}
	}
	if r := left["class:said"].Pages[0].Entries[0].Repeats; r.Count != 2 || r.Anchors != 2 {
		t.Fatalf("the repeats of a kept row: %+v", r)
	}
	if l := left["class:said"].Entry.Links; len(l.Out) != 1 || l.Out[0].Entry != nil {
		t.Fatalf("a kept source's link to a ref of one left out: kept, with no entry of it standing for its end: %+v", l)
	}
}

// A page's After(i) is the cursor after its entry i: the same rows follow
// it as follow a page that ended there.
func TestAfterIsTheCursorAfterAnEntry(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	for i, id := range []string{"a", "b", "c", "d"} {
		put(t, ix, "session:"+id, chat("session:"+id+"#e1", id, "the turnstile jammed "+manyWords(i+1), t0.Add(time.Duration(i)*time.Minute)))
	}
	for _, line := range []string{"turnstile", "class:said"} {
		q, _ := ParseQuery(line)
		whole, err := ix.Search(t.Context(), q)
		if err != nil || len(whole.Entries) != 4 {
			t.Fatalf("%q: %v %v", line, err, keysOf(whole.Entries))
		}
		for i := range 3 {
			q.Limit = i + 1
			short, err := ix.Search(t.Context(), q)
			if err != nil || short.Next != whole.After(i) {
				t.Fatalf("%q: the cursor after %d: %q, a page of %d ends at %q (%v)", line, i, whole.After(i), i+1, short.Next, err)
			}
			q.Limit, q.After = 0, whole.After(i)
			rest, err := ix.Search(t.Context(), q)
			if err != nil || !slices.Equal(keysOf(rest.Entries), keysOf(whole.Entries[i+1:])) {
				t.Fatalf("%q: after %d: %v, want %v (%v)", line, i, keysOf(rest.Entries), keysOf(whole.Entries[i+1:]), err)
			}
			q.After = ""
		}
		if whole.After(4) != "" || whole.After(-1) != "" {
			t.Fatalf("%q: a cursor past the page", line)
		}
	}
}
