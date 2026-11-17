package brakeman

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// keepBriefed starts a request and keeps b as its brief.
func (f *fixture) keepBriefed(op string, b Brief) Request {
	f.t.Helper()
	r, err := f.s.KeepBrief(f.start(op).ID, Kept{OpID: "b-" + op, Brief: b})
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// readDigest reads a request's digest sibling as it is on disk.
func (f *fixture) readDigest(id string) (Digest, bool) {
	f.t.Helper()
	b, err := os.ReadFile(f.reqPath(digestDoc(id)))
	if os.IsNotExist(err) {
		return Digest{}, false
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var d Digest
	if err := json.Unmarshal(b, &d); err != nil {
		f.t.Fatal(err)
	}
	return d, true
}

// TestAKeptBriefKeepsItsDigest: as a brief is kept its digest is written
// beside it: the title, the reading cut at a word within MaxReading with
// readingCut, the items counted by kind, and what they cite, each about
// list first cited first and at most MaxDigestAbout.
func TestAKeptBriefKeepsItsDigest(t *testing.T) {
	f := newFixture(t)
	words := strings.Repeat("word ", 80) // 400 characters
	b := Brief{Schema: BriefSchema, Title: "Build is out of attempts", Summary: words + "\n\nThe rest of the reading.", Items: []Item{
		factItem(t, "The run failed."),
		{Kind: ItemFact, Fact: &Fact{Text: "The task failed.", Evidence: []Ref{ref(t, "check:"+runA+"/build/2/vet/1"), ref(t, "ticket:CND-41")}}},
		{Kind: ItemInterpretation, Interpretation: &Interpretation{Text: "A flake.", Facts: []int{0, 1}}},
		decision(t), proposal(t),
		draftTicket(t, "CND", "Fix it"), draftCrew(), handoff(t),
		comparison(t), overlap(t),
		{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: "Comments", Refs: []Ref{ref(t, "comment:CND-42/"+commentA)}}},
		{Kind: ItemOutcome, Outcome: &Outcome{Text: "Retried.", Receipt: ref(t, "command:"+runA+"/retry.1")}},
	}}
	for i := range MaxDigestAbout + 5 {
		b.Items[0].Fact.Evidence = append(b.Items[0].Fact.Evidence, ref(t, fmt.Sprintf("run:r%d-0000000b", i)))
	}
	r := f.keepBriefed("op-1", b)
	d, ok := f.readDigest(r.ID)
	if !ok {
		t.Fatal("no digest beside the brief")
	}
	if d.Schema != DigestSchema || d.Title != b.Title {
		t.Fatalf("digest %+v", d)
	}
	if !d.ReadingCut || utf8.RuneCountInString(d.Reading) > MaxReading || !strings.HasSuffix(d.Reading, "word…") || !strings.HasPrefix(words, strings.TrimSuffix(d.Reading, "…")) {
		t.Fatalf("reading %q (%d characters), cut %v", d.Reading, utf8.RuneCountInString(d.Reading), d.ReadingCut)
	}
	want := DigestCounts{Decisions: 1, Proposals: 1, Drafts: 2, Handoffs: 1, Facts: 2, Interpretations: 1, Outcomes: 1, Overlaps: 1, Comparisons: 1, Evidence: 1}
	if d.Counts != want {
		t.Fatalf("counts %+v, want %+v", d.Counts, want)
	}
	if len(d.About.Runs) != MaxDigestAbout || d.About.Runs[0] != runA || d.About.Runs[1] != "r0-0000000b" {
		t.Fatalf("runs %v", d.About.Runs)
	}
	if !slices.Equal(d.About.Tasks, []string{runA + "/build"}) {
		t.Fatalf("tasks %v", d.About.Tasks)
	}
	if !slices.Equal(d.About.Sessions, []string{sessA}) {
		t.Fatalf("sessions %v", d.About.Sessions)
	}
	if !slices.Equal(d.About.Tickets, []string{"CND-41", "CND-1", "CND-42"}) {
		t.Fatalf("tickets %v", d.About.Tickets)
	}
	got, err := f.s.Digest(r.ID)
	if err != nil || got == nil || got.Title != d.Title || got.Counts != d.Counts {
		t.Fatalf("Digest answers %+v %v", got, err)
	}
	// A request with no brief has none.
	if got, err := f.s.Digest(f.create("op-2").ID); err != nil || got != nil {
		t.Fatalf("a queued request's digest: %+v %v", got, err)
	}
}

// TestTheReadingIsPlainText: the reading is the summary's first paragraph
// that holds text, a heading without its marks, its Markdown made text
// (a link its text, emphasis and code marks dropped), so a cut never
// leaves markup half drawn; a word's own underscores and a lone star are
// kept; a short one is not cut; a summary of no text gives "".
func TestTheReadingIsPlainText(t *testing.T) {
	for _, c := range []struct{ summary, want string }{
		{"One run failed.", "One run failed."},
		{"One run\nfailed.\n\nMore.", "One run failed."},
		{"\n\n  \n# The build\n\nIt failed.", "The build"},
		{"## Build is out ##\nof attempts", "Build is out of attempts"},
		{"See [the log](/runs/a/log) and ![a chart](x.png).", "See the log and a chart."},
		{"**Three** failures, _all_ on `cache_test`; ~~flaky~~.", "Three failures, all on cache_test; flaky."},
		{"fix_login and 3 * 4 and ~5 minutes", "fix_login and 3 * 4 and ~5 minutes"},
		{"> - Retry it\n> - or not", "Retry it or not"},
		{"A \\*literal\\* star and <https://example.com/x>.", "A *literal* star and https://example.com/x."},
		{"[not a link and ![no image", "[not a link and ![no image"},
		{"```\ncode\n```", "code"},
		{"---\n\n", ""},
		{"", ""},
	} {
		got, cut := readingOf(c.summary)
		if got != c.want || cut {
			t.Errorf("readingOf(%q) = %q %v, want %q", c.summary, got, cut, c.want)
		}
	}
	// Exactly MaxReading characters stays whole; one more is cut.
	at := strings.Repeat("é", MaxReading)
	if got, cut := readingOf(at); got != at || cut {
		t.Fatalf("at the bound: %d characters, cut %v", utf8.RuneCountInString(got), cut)
	}
	long := strings.Repeat("a ", MaxReading) // 560 characters
	got, cut := readingOf("[" + long + "](/x)")
	if !cut || utf8.RuneCountInString(got) > MaxReading || strings.ContainsAny(got, "[]()") || !strings.HasSuffix(got, "a…") {
		t.Fatalf("a long link: %q %v", got, cut)
	}
	// One word past the bound is cut within it.
	got, cut = readingOf(strings.Repeat("x", MaxReading+1))
	if !cut || utf8.RuneCountInString(got) != MaxReading || !strings.HasSuffix(got, "x…") {
		t.Fatalf("one long word: %d characters %v", utf8.RuneCountInString(got), cut)
	}
}

// TestADigestIsASiblingOpenKeeps: a kept brief's digest outlives a
// reopen, and the request's document names no digest.
func TestADigestIsASiblingOpenKeeps(t *testing.T) {
	f := newFixture(t)
	r := f.keepBriefed("op-1", goodBrief(t))
	f.reopen()
	if !exists(t, f.reqPath(digestDoc(r.ID))) {
		t.Fatal("the reopen removed the digest")
	}
	if d, err := f.s.Digest(r.ID); err != nil || d == nil || d.Title != "Overnight" || d.Reading != "One run failed." || d.Counts.Facts != 1 {
		t.Fatalf("after the reopen: %+v %v", d, err)
	}
}

// TestPruneRemovesTheDigest: a pruned request's digest goes with it, and
// the store answers none for it after.
func TestPruneRemovesTheDigest(t *testing.T) {
	f := newFixture(t)
	gone := f.keepBriefed("op-1", goodBrief(t))
	kept := f.keepBriefed("op-2", goodBrief(t))
	if _, _, err := f.s.ListWhere("", MaxPage, nil); err != nil { // the cache holds both
		t.Fatal(err)
	}
	if n, err := f.s.PruneKeeping(1); err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	if exists(t, f.reqPath(digestDoc(gone.ID))) {
		t.Fatal("a pruned request's digest stayed")
	}
	if !exists(t, f.reqPath(digestDoc(kept.ID))) {
		t.Fatal("the kept request's digest went")
	}
	f.s.mu.Lock()
	_, cached := f.s.digests[gone.ID]
	f.s.mu.Unlock()
	if cached {
		t.Fatal("the pruned request's digest is still held")
	}
}

// TestABriefKeptWithoutADigestIsDigestedWhenListed: a brief kept before
// digests were (no sibling) is digested from its brief when it is listed:
// written back by a store that owns the records, made and not written by
// one that does not; a sibling that does not read is kept as it is and
// the brief's digest answered; and a second list reads neither the brief
// nor the sibling again.
func TestABriefKeptWithoutADigestIsDigestedWhenListed(t *testing.T) {
	f := newFixture(t)
	old := f.keepBriefed("op-1", goodBrief(t))
	if err := os.Remove(f.reqPath(digestDoc(old.ID))); err != nil {
		t.Fatal(err)
	}
	// One that does not own the records derives it and writes nothing.
	second := f.open()
	if ro, _ := second.ReadOnly(); !ro {
		t.Fatal("the second store owns the records")
	}
	page, _, err := second.ListWhere("", MaxPage, nil)
	if err != nil || len(page) != 1 || page[0].Digest == nil || page[0].Digest.Title != "Overnight" {
		t.Fatalf("listed by the second store: %+v %v", page, err)
	}
	if exists(t, f.reqPath(digestDoc(old.ID))) {
		t.Fatal("a store that does not own the records wrote a digest")
	}
	second.Close() // before the owner's reopen, so the lock comes back to it
	// The owner writes it back.
	f.reopen()
	page, _, err = f.s.ListWhere("", MaxPage, nil)
	if err != nil || len(page) != 1 || page[0].Digest == nil || page[0].Digest.Reading != "One run failed." {
		t.Fatalf("listed by the owner: %+v %v", page, err)
	}
	if d, ok := f.readDigest(old.ID); !ok || d.Title != "Overnight" {
		t.Fatalf("the owner wrote back %+v %v", d, ok)
	}
	// Held: the brief and the sibling gone, the next list still answers it.
	for _, name := range []string{briefDoc(old.ID), digestDoc(old.ID)} {
		if err := os.Remove(f.reqPath(name)); err != nil {
			t.Fatal(err)
		}
	}
	if page, _, err = f.s.ListWhere("", MaxPage, nil); err != nil || page[0].Digest == nil || page[0].Digest.Title != "Overnight" {
		t.Fatalf("a second list read the files again: %+v %v", page, err)
	}
	// A sibling that does not read is kept as it is.
	damaged := f.keepBriefed("op-2", goodBrief(t))
	if err := os.WriteFile(f.reqPath(digestDoc(damaged.ID)), []byte(`{"schema":1,"later":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	d, err := f.s.Digest(damaged.ID)
	if err != nil || d == nil || d.Title != "Overnight" {
		t.Fatalf("a damaged sibling's request: %+v %v", d, err)
	}
	if b, _ := os.ReadFile(f.reqPath(digestDoc(damaged.ID))); string(b) != `{"schema":1,"later":true}` {
		t.Fatalf("the damaged sibling was rewritten: %s", b)
	}
	// A request whose brief is gone, and none held, has no digest.
	if d, err := f.s.Digest(old.ID); err != nil || d != nil {
		t.Fatalf("no brief and no sibling: %+v %v", d, err)
	}
}

// TestTheRequestDocumentIsUnchangedByADigest: a briefed request's
// document holds the keys it held before digests were, none of the
// digest's, so an older build reads it as it did.
func TestTheRequestDocumentIsUnchangedByADigest(t *testing.T) {
	f := newFixture(t)
	r := f.keepBriefed("op-1", goodBrief(t))
	b, err := os.ReadFile(f.reqPath(requestDoc(r.ID)))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range doc {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"agent", "briefAt", "briefOpId", "by", "closedAt", "config", "createdAt", "deadline", "footprint", "hasBrief", "id", "kind", "opId", "reads", "schema", "state"}
	if !slices.Equal(keys, want) {
		t.Fatalf("the request's keys %v, want %v", keys, want)
	}
	// And the brief's document is the brief as given.
	var kept Brief
	if ok, err := f.s.reqs.LoadLimit(briefDoc(r.ID), &kept, MaxBrief); err != nil || !ok || kept.Title != "Overnight" {
		t.Fatalf("the brief: %+v %v", kept, err)
	}
}

// TestADigestAtItsLargestFitsItsLoadLimit: a digest with the longest
// title and reading, every count and each about list full of the longest
// ids is within MaxDigestDoc as written, and is read back whole.
func TestADigestAtItsLargestFitsItsLoadLimit(t *testing.T) {
	f := newFixture(t)
	// Characters JSON escapes: six bytes each in the title, two in the
	// reading.
	title := strings.Repeat("\u2028", MaxTitle)
	b := Brief{Schema: BriefSchema, Title: title, Summary: strings.Repeat(`""" `, MaxReading)}
	var ev []Ref
	for i := range MaxDigestAbout {
		run := fmt.Sprintf("%063d-%08x", i, i)
		ev = append(ev,
			ref(t, "task:"+run+"/"+strings.Repeat("t", 40)),
			ref(t, fmt.Sprintf("session:%016x", i)),
			ref(t, fmt.Sprintf("ticket:ABCDEF-%d", int64(1e17)+int64(i))))
	}
	b.Items = []Item{{Kind: ItemFact, Fact: &Fact{Text: "f", Evidence: ev}}}
	r := f.keepBriefed("op-1", b)
	fi, err := os.Stat(f.reqPath(digestDoc(r.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > MaxDigestDoc || fi.Size() < MaxDigestDoc/4 {
		t.Fatalf("the digest is %d bytes against its bound %d", fi.Size(), MaxDigestDoc)
	}
	f.reopen()
	f.s.mu.Lock()
	f.s.digests = nil
	f.s.mu.Unlock()
	d, err := f.s.Digest(r.ID)
	if err != nil || d == nil || d.Title != title || d.Counts.Facts != 1 || len(d.About.Tasks) != MaxDigestAbout || len(d.About.Tickets) != MaxDigestAbout {
		t.Fatalf("read back: %v", err)
	}
	if _, ok := f.readDigest(r.ID); !ok {
		t.Fatal("gone")
	}
}
