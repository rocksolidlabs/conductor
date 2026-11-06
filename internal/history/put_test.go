package history

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// Every kind of the table is of its class, set by the index; a class a
// caller gives is refused, and so is a kind the table does not hold. The
// expected table is the plan's ("The kinds and their class"), written
// out here, so a change to kinds.go fails until this test is changed
// with it.
func TestClassComesFromTheKindAlone(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	want := map[string]string{
		"session": ClassRecord, "run": ClassRecord, "task": ClassRecord, "attempt": ClassRecord,
		"ticket": ClassRecord, "plan": ClassRecord, "launch": ClassRecord, "step": ClassRecord,
		"session.typed": ClassSaid, "session.answer": ClassSaid, "session.question": ClassSaid, "session.report": ClassSaid,
		"session.file": ClassSaid, "session.chat": ClassSaid, "attempt.result": ClassSaid, "review.verdict": ClassSaid,
		"review.finding": ClassSaid, "attempt.checkpoint": ClassSaid, "attempt.blocker": ClassSaid, "message": ClassSaid,
		"comment": ClassSaid, "brief.item": ClassSaid,
		"command": ClassDecided, "plan.approved": ClassDecided, "plan.withdrawn": ClassDecided, "plan.superseded": ClassDecided,
		"ticket.accepted": ClassDecided, "ticket.unaccepted": ClassDecided, "ticket.closed": ClassDecided,
		"check": ClassChecked, "verify": ClassChecked, "candidate.verified": ClassChecked, "delivery.verified": ClassChecked,
	}
	if got := Kinds(); len(got) != len(want) {
		t.Fatalf("the table holds %d kinds, the plan %d: %v", len(got), len(want), got)
	}
	var entries []Entry
	for kind := range want {
		entries = append(entries, Entry{Key: "k:" + kind, Kind: kind, Ref: "r:" + kind, Anchor: "a", Title: "the same words",
			Body: "the same words", At: t0, Commit: "abc1234", Actor: "admin", Outcome: "passed"})
	}
	if err := ix.Replace(ctx, Source{Key: "s"}, entries, nil); err != nil {
		t.Fatal(err)
	}
	for kind, class := range want {
		h, err := ix.Entry(ctx, "k:"+kind)
		if err != nil {
			t.Fatal(err)
		}
		if h.Class != class {
			t.Errorf("%s: %s, want %s", kind, h.Class, class)
		}
	}
	// The same words in every class: none is another's repeat.
	if h, _ := ix.Entry(ctx, "k:check"); h.Repeats.Count != 4 {
		t.Errorf("a check's repeats: %+v, want the checked kinds' 4", h.Repeats)
	}
	e := Entry{Key: "k", Kind: "session.chat", Class: ClassChecked, Ref: "r", Anchor: "a", Title: "t", At: t0}
	wantErr(t, ix.Replace(ctx, Source{Key: "s2"}, []Entry{e}, nil), ErrClass)
	e.Class, e.Kind = "", "session.verdict"
	wantErr(t, ix.Replace(ctx, Source{Key: "s2"}, []Entry{e}, nil), ErrKind)
}

// The same sentence said in ten sessions is ten said entries, each saying
// it was said ten times in ten anchors; a receipt holding the same words
// is not among their repeats, nor they among its.
func TestARepeatedSentenceStaysSaid(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	const sentence = "The login redirect is fixed now."
	for i := range 10 {
		s := fmt.Sprintf("%016x", i)
		put(t, ix, "session:"+s, chat("session:"+s+"#e1", s, sentence, t0.Add(time.Duration(i)*time.Minute)))
	}
	put(t, ix, "check:r/build/1/unit/1", receipt("check:r/build/1/unit/1", "r", "sub", "passed", "the LOGIN redirect is   fixed now.", t0))
	p := search(t, ix, "login redirect", MaxLimit)
	if len(p.Entries) != 11 {
		t.Fatalf("%d entries, want 11", len(p.Entries))
	}
	for _, h := range p.Entries {
		switch {
		case h.Kind == "check":
			if h.Class != ClassChecked || h.Repeats != (Repeats{1, 1}) {
				t.Errorf("the receipt: %s %+v", h.Class, h.Repeats)
			}
		case h.Class != ClassSaid || h.Repeats != (Repeats{10, 10}):
			t.Errorf("%s: %s %+v, want said, 10 in 10", h.Key, h.Class, h.Repeats)
		}
	}
}

// A Replace whose commit fails, or one refused before its transaction,
// leaves the source's old rows and their search rows whole.
func TestReplaceIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "oldword", t0), chat("session:s1#e2", "s1", "oldword again", t0))
	ix.beforeCommit = func() error { return errors.New("the disk is full") }
	err := ix.Replace(ctx, Source{Key: "session:s1", Mark: "m2"}, []Entry{chat("session:s1#e1", "s1", "newword", t0)}, nil)
	if err == nil {
		t.Fatal("a failed commit answered no error")
	}
	ix.beforeCommit = nil
	// Refused before the transaction: a bad kind among good entries, and a
	// key another source holds.
	bad := chat("session:s1#e3", "s1", "newword", t0)
	bad.Kind = "nope"
	wantErr(t, ix.Replace(ctx, Source{Key: "session:s1", Mark: "m2"}, []Entry{chat("session:s1#e1", "s1", "newword", t0), bad}, nil), ErrKind)
	put(t, ix, "session:s2", chat("session:s2#e1", "s2", "other", t0))
	wantErr(t, ix.Replace(ctx, Source{Key: "session:s1", Mark: "m2"}, []Entry{chat("session:s2#e1", "s1", "newword", t0)}, nil), ErrKeyTaken)
	wantSameKeys(t, ix, "oldword", "session:s1#e1", "session:s1#e2")
	wantKeys(t, ix, "newword")
	wantKeys(t, ix, "other", "session:s2#e1")
	if mark, ok, err := ix.Mark(ctx, "session:s1"); err != nil || !ok || mark != "m1" {
		t.Fatalf("the mark: %q %v %v", mark, ok, err)
	}
	ftsOK(t, ix)
}

// Remove takes a source's entries, links and search rows with it, and
// leaves the other sources' as they were.
func TestRemoveCascades(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	links := []Link{{From: "check:r/build/1/unit/1", Rel: "handed", To: "attempt:r/build/2"}, {From: "session:s9", Rel: "resumed_from", To: "session:s1"}}
	if err := ix.Replace(ctx, Source{Key: "run:r", Mark: "L1"}, []Entry{receipt("check:r/build/1/unit/1", "r", "sub", "failed", "alpha", t0),
		{Key: "attempt:r/build/2", Kind: "attempt", Ref: "attempt:r/build/2", Anchor: "task:r/build", Title: "build #2", Body: "alpha", At: t0}}, links); err != nil {
		t.Fatal(err)
	}
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	if l, err := ix.Links(ctx, "attempt:r/build/2", 0); err != nil || len(l.In) != 1 || l.In[0].Entry == nil || l.In[0].Entry.Outcome != "failed" {
		t.Fatalf("the link in, before: %+v %v", l, err)
	}
	if err := ix.Remove(ctx, "run:r"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"check:r/build/1/unit/1", "attempt:r/build/2"} {
		if _, err := ix.Entry(ctx, key); !errors.Is(err, ErrGone) {
			t.Errorf("%s: %v, want gone", key, err)
		}
	}
	if l, _ := ix.Links(ctx, "session:s1", 0); len(l.In)+len(l.Out) != 0 {
		t.Errorf("a link kept: %+v", l)
	}
	if _, ok, _ := ix.Mark(ctx, "run:r"); ok {
		t.Error("the source kept")
	}
	wantKeys(t, ix, "alpha", "session:s1#e1")
	if n := count(t, ix, "links"); n != 0 {
		t.Errorf("%d links", n)
	}
	ftsOK(t, ix)
	if err := ix.Remove(ctx, "run:none"); err != nil {
		t.Fatalf("removing a source not held: %v", err)
	}
}

// Past the budget the oldest entries go first, by when they happened and
// never by class: a source wholly older than the cut goes whole (its
// links with it, its row and mark kept), another loses only its entries
// past it; Stats says from when the index holds history and how many
// went. By entries, then by bytes.
func TestTheBudgetDropsTheOldestFirst(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{MaxEntries: 10})
	at := func(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }
	source := func(name string, from int, link bool) {
		var es []Entry
		for i := range 4 {
			e := chat(fmt.Sprintf("session:%s#e%d", name, i), name, "alpha", at(from+i))
			if i%2 == 0 {
				e = receipt(fmt.Sprintf("check:%s/%d", name, i), name, "sub", "passed", "alpha", at(from+i))
			}
			es = append(es, e)
		}
		var links []Link
		if link {
			links = []Link{{From: "session:" + name, Rel: "resumed_from", To: "session:x"}}
		}
		if err := ix.Replace(ctx, Source{Key: "session:" + name, Mark: "m-" + name}, es, links); err != nil {
			t.Fatal(err)
		}
	}
	source("a", -10, true) // -10 .. -7
	source("b", -6, false) // -6 .. -3
	source("c", -2, false) // -2 .. +1: 12 entries, two go
	st, err := ix.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Entries != 10 || st.Dropped != 2 || !st.Oldest.Equal(at(-8)) {
		t.Fatalf("after one cut: %+v", st)
	}
	if l, _ := ix.Links(ctx, "session:a", 0); len(l.Out) != 1 {
		t.Fatal("a source cut in part lost its link")
	}
	source("d", 2, false) // 14: four go, a's last two and b's first two
	st, _ = ix.Stats(ctx)
	if st.Entries != 10 || st.Dropped != 6 || !st.Oldest.Equal(at(-4)) {
		t.Fatalf("after two cuts: %+v", st)
	}
	if l, _ := ix.Links(ctx, "session:a", 0); len(l.Out) != 0 {
		t.Fatal("a source gone whole kept its link")
	}
	if mark, ok, _ := ix.Mark(ctx, "session:a"); !ok || mark != "m-a" {
		t.Fatalf("a source gone whole lost its mark: %q %v", mark, ok)
	}
	ftsOK(t, ix)

	// By bytes: 4 entries of 30 bytes each under 100.
	ix = openIndex(t, t.TempDir(), Options{MaxBytes: 100})
	var es []Entry
	for i := range 4 {
		e := chat(fmt.Sprintf("session:s#e%d", i), "s", strings.Repeat("b", 20), at(i))
		e.Title = strings.Repeat("t", 10)
		es = append(es, e)
	}
	put(t, ix, "session:s", es...)
	st, _ = ix.Stats(ctx)
	if st.Entries != 3 || st.Bytes != 90 || st.Dropped != 1 || !st.Oldest.Equal(at(1)) {
		t.Fatalf("by bytes: %+v", st)
	}
	ftsOK(t, ix)
}

// A key answers the same evidence after its source is read again and
// after the index is rebuilt, or entry_gone once its source is removed,
// never another entry: among them a path read and then edited in one
// session (two Files rows, two keys) and a revision approved twice (two
// decisions, two keys).
func TestAKeyOutlivesReplaceAndReindex(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	file := func(op string) Entry {
		return Entry{Key: "session:s1#f" + op + "/9f2c", Kind: "session.file", Ref: "session:s1", Anchor: "session:s1", Title: "web/app/x.ts", Summary: op, At: t0}
	}
	approval := func(op string) Entry {
		return Entry{Key: "ticket:CND-1#plan.approved/1/" + op, Kind: "plan.approved", Ref: "plan:CND-1/1", Anchor: "ticket:CND-1",
			Title: "r1 approved", Actor: "admin", Body: "intent " + op, At: t0, Project: "CND", Ticket: "CND-1"}
	}
	session := func(words string) []Entry {
		return []Entry{
			{Key: "session:s1", Kind: "session", Ref: "session:s1", Anchor: "session:s1", Title: "fix login", At: t0},
			file("read"), file("edit"), chat("session:s1#e3", "s1", words, t0),
		}
	}
	tickets := []Entry{{Key: "ticket:CND-1", Kind: "ticket", Ref: "ticket:CND-1", Anchor: "ticket:CND-1", Title: "Login", At: t0}, approval("op-a"), approval("op-b")}
	put(t, ix, "session:s1", session("first words")...)
	put(t, ix, "ticket:CND-1", tickets...)
	keys := keysOf(search(t, ix, "", MaxLimit).Entries)
	if len(keys) != 7 {
		t.Fatalf("keys %v", keys)
	}
	took := map[string]Entry{}
	for _, k := range keys {
		h, err := ix.Entry(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		took[k] = h.Entry
	}
	same := func(why string, changed ...string) {
		t.Helper()
		for _, k := range keys {
			h, err := ix.Entry(ctx, k)
			if err != nil {
				t.Fatalf("%s: %s: %v", why, k, err)
			}
			if slices.Contains(changed, k) {
				continue
			}
			if h.Entry != took[k] {
				t.Errorf("%s: %s answers %+v, was %+v", why, k, h.Entry, took[k])
			}
		}
	}
	put(t, ix, "session:s1", session("second words")...)
	same("read again", "session:s1#e3")
	if h, _ := ix.Entry(ctx, "session:s1#e3"); h.Body != "second words" {
		t.Errorf("a key of a source read again answers %q", h.Body)
	}
	if err := ix.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	put(t, ix, "ticket:CND-1", tickets...)
	put(t, ix, "session:s1", session("first words")...)
	same("rebuilt")
	if err := ix.Remove(ctx, "ticket:CND-1"); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		_, err := ix.Entry(ctx, k)
		if strings.HasPrefix(k, "ticket:") != errors.Is(err, ErrGone) {
			t.Errorf("%s after its source's removal: %v", k, err)
		}
	}
}

// What Replace refuses: a text past its bound, a receipt with no commit,
// a decision with no actor, a ref with a control character, a key given
// twice, text that is not UTF-8.
func TestReplaceRefusesPastABound(t *testing.T) {
	ctx := context.Background()
	ix := openIndex(t, t.TempDir(), Options{})
	ok := chat("session:s#e1", "s", "words", t0)
	for name, change := range map[string]func(e *Entry){
		"title":     func(e *Entry) { e.Title = strings.Repeat("t", MaxTitle+1) },
		"summary":   func(e *Entry) { e.Summary = strings.Repeat("s", MaxSummary+1) },
		"body":      func(e *Entry) { e.Body = strings.Repeat("b", MaxShortBody+1) },
		"commit":    func(e *Entry) { *e = receipt("check:r/1", "r", "sub", "passed", "", t0); e.Commit = "" },
		"actor":     func(e *Entry) { e.Kind = "command" },
		"control":   func(e *Entry) { e.Ref = "session:s\n" },
		"anchor":    func(e *Entry) { e.Anchor = "" },
		"no time":   func(e *Entry) { e.At = time.Time{} },
		"not UTF-8": func(e *Entry) { e.Body = "\xff" },
		"long ref":  func(e *Entry) { e.Ref = strings.Repeat("r", MaxRef+1) },
	} {
		e := ok
		change(&e)
		if err := ix.Replace(ctx, Source{Key: "s"}, []Entry{e}, nil); !errors.Is(err, ErrBound) {
			t.Errorf("%s: %v, want a bound", name, err)
		}
	}
	long := ok
	long.Kind, long.Body = "comment", strings.Repeat("b", MaxLongBody)
	if err := ix.Replace(ctx, Source{Key: "s"}, []Entry{long}, nil); err != nil {
		t.Errorf("a comment at its bound: %v", err)
	}
	wantErr(t, ix.Replace(ctx, Source{Key: "s"}, []Entry{ok, ok}, nil), ErrBound)
	wantErr(t, ix.Replace(ctx, Source{Key: "s"}, nil, []Link{{From: "a", Rel: "Bad Rel", To: "b"}}), ErrBound)
}

// A check's summary is its argv cut to 1 KiB with its count; its body a
// long log's first 4,096 and last 12,283 bytes about the separator,
// exactly 16,384 bytes, each end cut at a rune boundary.
func TestCheckSummaryAndLog(t *testing.T) {
	argv := make([]string, 64)
	for i := range argv {
		argv[i] = strings.Repeat("é", 40)
	}
	s := CheckSummary(argv)
	if len(s) > MaxSummary || !utf8.ValidString(s) || !strings.HasSuffix(s, "… (64 arguments)") {
		t.Fatalf("the summary: %d bytes %q", len(s), s[len(s)-30:])
	}
	if s := CheckSummary([]string{"go", "test", "./..."}); s != "go test ./..." {
		t.Fatalf("a short argv: %q", s)
	}
	log := "HEAD" + strings.Repeat("x", 1<<20) + "TAIL"
	b := CheckLog(log)
	if len(b) != MaxCheckBody || !strings.HasPrefix(b, "HEAD") || !strings.HasSuffix(b, "TAIL") || !strings.Contains(b, "\n…\n") {
		t.Fatalf("the body: %d bytes", len(b))
	}
	multi := strings.Repeat("é", 1<<15)
	if b := CheckLog(multi); !utf8.ValidString(b) || len(b) > MaxCheckBody {
		t.Fatalf("a body of two-byte runes: %d bytes, valid %v", len(b), utf8.ValidString(b))
	}
	if b := CheckLog("short\xfflog"); !utf8.ValidString(b) {
		t.Fatal("a log of invalid bytes made invalid text")
	}
}

// Writers and readers at once, under -race: every entry put is found, and
// the search table agrees with the entries.
func TestReplaceAndSearchConcurrently(t *testing.T) {
	ix := openIndex(t, t.TempDir(), Options{})
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 10 {
				s := fmt.Sprintf("%08x%08x", w, i)
				if err := ix.Replace(context.Background(), Source{Key: "session:" + s}, []Entry{chat("session:"+s+"#e1", s, "gamma delta", t0)}, nil); err != nil {
					t.Error(err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 10 {
				q, _ := ParseQuery("gamma")
				if _, err := ix.Search(context.Background(), q); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if p := search(t, ix, "gamma", 0); p.Total != 80 {
		t.Fatalf("%d found, want 80", p.Total)
	}
	ftsOK(t, ix)
}
