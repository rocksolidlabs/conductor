package history

import (
	"context"
	"errors"
	"fmt"
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
		{"alpha class:checked outcome:failed", []string{"b"}}, {"class:said class:checked", nil},
	} {
		wantSameKeys(t, ix, c.line, c.want...)
	}
	for _, line := range []string{
		"class:bogus", "kind:nope", "outcome:Bad!", "project:1X", "project:TOOLONGKEY", "repo:/", "agent:Bad_Agent!", "crew:-x",
		"crew:" + longCrew + "x", "crew:a_b", "run:-x", "run:team-1", "run:review-0a1b2c3", "run:" + longCrew + "x-0123abcd", "ticket:CND-0", "ticket:CND", "session:xyz", "since:yesterday", "until:7y", "since:0d", "since:1d since:2d",
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
	if s := clipSnippet(strings.Repeat("w ", 100)+snipOpen+strings.Repeat("m", 400)+snipClose, MaxSnippet); !strings.HasPrefix(s, "[mmm") || len(s) > MaxSnippet {
		t.Errorf("a match longer than the bound: %q (%d bytes)", s, len(s))
	}
	w := strings.Repeat("word ", 50)
	if s := clipSnippet(w+snipOpen+"hit"+snipClose+" "+w+snipOpen+"two"+snipClose+w, MaxSnippet); strings.Count(s, "[") != strings.Count(s, "]") {
		t.Errorf("a mark cut through: %q", s)
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
