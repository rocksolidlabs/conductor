package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/export"
)

// testIndex is an index in a directory of the test's, closed at its end.
func testIndex(t *testing.T, dir string, bound int64) *outputIndex {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	x, err := openOutputIndex(dir, bound)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.close() })
	// A fresh index answers once rebuilt: here, from no source.
	x.rebuild(nil)
	return x
}

// quotesOf asks the index whether text quotes a source, once it has
// indexed what it was fed: the test waits for that (a megabyte under the
// race detector takes longer than the product's own wait, whose answer is
// pending), up to testWait.
func quotesOf(t *testing.T, x *outputIndex, text string) bool {
	t.Helper()
	// The index's own wait (whole, its 2 s for the feeds in flight) is
	// asked again until it is whole: a large source under the race
	// detector on a loaded machine is indexed slowly, and the test's
	// timeout bounds a hang. An index that cannot be whole now fails.
	for !x.whole(context.Background()) {
		if why := x.state(); why != "" {
			t.Fatalf("the index is not whole: %s", why)
		}
	}
	q, err := x.quotes(text)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// words is n bytes of random words of lower-case letters, each a word of
// its own so no 64-byte run repeats by chance.
func words(r *rand.Rand, n int) string {
	var b strings.Builder
	for b.Len() < n {
		w := 2 + r.IntN(9)
		for range w {
			b.WriteByte(byte('a' + r.IntN(26)))
		}
		b.WriteByte(' ')
	}
	return b.String()[:n]
}

// spaced is s with its whitespace varied: each space one to three of
// spaces, tabs and line ends.
func spaced(r *rand.Rand, s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c != ' ' {
			b.WriteByte(c)
			continue
		}
		for range 1 + r.IntN(3) {
			b.WriteByte(" \t\n"[r.IntN(3)])
		}
	}
	return b.String()
}

// Any run of 64 to 200 bytes of a source, at any offset, its whitespace
// varied, inside other words, is caught: the source fed as a session's
// output in chunks of any size with escape sequences between its bytes,
// or kept whole as a check's log.
func TestWinnowingCatchesEvery64ByteQuote(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	x := testIndex(t, "", 0)
	sources := map[string]string{}
	for i := range 8 {
		text := words(r, 2000+r.IntN(2000))
		name := "ring:s" + string(rune('a'+i))
		sources[name] = text
		// The output in chunks, an escape sequence now and then.
		var fed strings.Builder
		for j := 0; j < len(text); {
			n := min(len(text)-j, 1+r.IntN(300))
			fed.WriteString(text[j : j+n])
			if r.IntN(3) == 0 {
				fed.WriteString([]string{"\x1b[31m", "\x1b[0m", "\x1b]0;title\x07", "\x1b[2K"}[r.IntN(4)])
			}
			x.feed(name, []byte(fed.String()), 1<<20)
			fed.Reset()
			j += n
		}
	}
	// A source that repeats itself (a test's output, line after line):
	// ties between windows of one text, which the picks must survive.
	var rep strings.Builder
	block := words(r, 150)
	for range 20 {
		rep.WriteString(block)
		rep.WriteString(words(r, 1+r.IntN(40)))
	}
	sources["ring:repeats"] = rep.String()
	x.feed("ring:repeats", []byte(rep.String()), 1<<20)
	kept := words(r, 3000)
	x.keepText("check:r1/t1/1/test/1", []byte(kept))
	x.reader("check", func(id string) ([]byte, bool) { return []byte(kept), id == "r1/t1/1/test/1" })
	sources["check:r1/t1/1/test/1"] = kept
	var names []string
	for n := range sources {
		names = append(names, n)
	}
	for trial := range 300 {
		src := sources[names[r.IntN(len(names))]]
		n := 64 + r.IntN(137)
		off := r.IntN(len(src) - n)
		quote := spaced(r, src[off:off+n])
		value := "Before " + words(r, r.IntN(80)) + "\n" + quote + "\n after " + words(r, r.IntN(80))
		if !quotesOf(t, x, value) {
			t.Fatalf("trial %d: a quote of %d bytes at %d was not caught: %q", trial, n, off, quote)
		}
	}
}

// At the threshold: a run of 64 bytes is caught and one of 63 is not; two
// whole lines of 16 bytes or more are caught, one is not, and two of 15
// are not.
func TestQuoteAtTheThresholdCaught(t *testing.T) {
	x := testIndex(t, "", 0)
	r := rand.New(rand.NewPCG(3, 4))
	text := words(r, 4000)
	out := "--- FAIL: TestSomething (0.01s)\nexpected widget count 3, got 2\nok  	example.com/pkg	0.2s\nshort line here\n"
	x.feed("ring:a", []byte(text+"\n"+out), 1<<20)
	run := func(n int) string { return "QQ" + text[1000:1000+n] + "QQ" }
	if !quotesOf(t, x, run(64)) {
		t.Fatal("64 bytes not caught")
	}
	if quotesOf(t, x, run(63)) {
		t.Fatal("63 bytes caught")
	}
	if !quotesOf(t, x, "My note:\n--- FAIL: TestSomething (0.01s)\nexpected widget count 3, got 2\nthat is all") {
		t.Fatal("two whole lines not caught")
	}
	if quotesOf(t, x, "My note:\n--- FAIL: TestSomething (0.01s)\nthat is all") {
		t.Fatal("one line caught")
	}
	if quotesOf(t, x, "short line here\nshort line here") {
		t.Fatal("two lines of 15 bytes caught")
	}
}

// A kept source's fingerprints are in the file: an index opened again,
// its memory and its cache empty, still catches a quote of it, confirmed
// through the source's reader. A session's ring went with the process
// before it, and is let go at the open with its prune recorded.
func TestQuoteFromOutsideTheMemoryCacheCaught(t *testing.T) {
	dir := t.TempDir()
	r := rand.New(rand.NewPCG(5, 6))
	log, ring := words(r, 3000), words(r, 3000)
	x, err := openOutputIndex(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	x.rebuild(nil)
	x.keepText("check:r1", []byte(log))
	x.feed("ring:s1", []byte(ring), 1<<20)
	if !x.whole(context.Background()) {
		t.Fatal("not whole")
	}
	x.close()
	before := time.Now().Add(-time.Second)
	y := testIndex(t, dir, 0)
	y.reader("check", func(id string) ([]byte, bool) { return []byte(log), id == "r1" })
	if len(y.mem) != 0 || len(y.cache) != 0 {
		t.Fatalf("a fresh index holds %d in memory, %d cached", len(y.mem), len(y.cache))
	}
	if !quotesOf(t, y, "a quote: "+log[500:600]) {
		t.Fatal("a quote of a kept source not caught from the file")
	}
	if quotesOf(t, y, "a quote: "+ring[500:600]) {
		t.Fatal("a ring of the process before caught")
	}
	if pruned, err := y.prunedSince(before); err != nil || !pruned {
		t.Fatalf("the ring's prune at the open: %v %v", pruned, err)
	}
	// A hit its reader no longer confirms (the text changed under it) does
	// not count; one with no reader counts.
	y.reader("check", func(string) ([]byte, bool) { return []byte(words(r, 3000)), true })
	y.cache = map[uint64][]fpRef{}
	if quotesOf(t, y, "a quote: "+log[500:600]) {
		t.Fatal("a hit the source's text does not hold counted")
	}
	delete(y.readers, "check")
	if !quotesOf(t, y, "a quote: "+log[500:600]) {
		t.Fatal("a hit that cannot be confirmed did not count")
	}
}

// An index past its bound answers pending for every text the rule could
// catch, and says so; one behind the output (fed, not indexed past the
// wait) is pending too; one whose file cannot be opened is pending.
func TestSaturatedIndexFailsClosed(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	x := testIndex(t, "", 1)
	x.feed("ring:a", []byte(words(r, 2000)), 1<<20)
	g := &outputGuard{index: x}
	for _, text := range []string{"a clean sentence of more than sixteen bytes", words(r, 200)} {
		if v := g.Check(context.Background(), text); v != verdictPending {
			t.Fatalf("past its bound, %q: %s", text, v)
		}
	}
	if !strings.Contains(x.state(), "over its bound") {
		t.Fatalf("the state: %q", x.state())
	}

	y := testIndex(t, "", 0)
	gate := make(chan struct{})
	y.enqueue(func() { <-gate })
	y.feed("ring:b", []byte(words(r, 500)), 1<<20)
	if v := (&outputGuard{index: y}).Check(context.Background(), "a clean sentence of more than sixteen bytes"); v != verdictPending {
		t.Fatalf("behind its feeds: %s", v)
	}
	close(gate)
	if v := (&outputGuard{index: y}).Check(context.Background(), "a clean sentence of more than sixteen bytes"); v != verdictClean {
		t.Fatalf("caught up: %s", v)
	}

	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	z := testIndex(t, bad, 0) // a file where the directory should be
	if v := (&outputGuard{index: z}).Check(context.Background(), "a clean sentence of more than sixteen bytes"); v != verdictPending || z.state() == "" {
		t.Fatalf("an index that cannot open: %s %q", v, z.state())
	}
}

// A source pruned takes its fingerprints with it, the prune's time
// recorded; a ring overwritten takes those of what it no longer holds.
func TestIndexPrunedWithItsSource(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	x := testIndex(t, "", 0)
	a := words(r, 3000)
	x.feed("ring:a", []byte(a), 1<<20)
	if !quotesOf(t, x, a[100:200]) {
		t.Fatal("not caught before the prune")
	}
	before := time.Now().Add(-time.Millisecond)
	if pruned, _ := x.prunedSince(before); pruned {
		t.Fatal("a prune before any")
	}
	x.prune("ring:a")
	if quotesOf(t, x, a[100:200]) {
		t.Fatal("caught after its source was pruned")
	}
	if pruned, err := x.prunedSince(before); err != nil || !pruned {
		t.Fatalf("the prune's time: %v %v", pruned, err)
	}
	var n int
	if err := x.db.QueryRow("SELECT COUNT(*) FROM fp WHERE source = 'ring:a'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d fingerprints left in the file %v", n, err)
	}
	// A ring of 1,000 bytes fed 4,000: the first are no longer held.
	b := words(r, 4000)
	for i := 0; i < len(b); i += 100 {
		x.feed("ring:b", []byte(b[i:i+100]), 1000)
	}
	if quotesOf(t, x, b[100:200]) {
		t.Fatal("a quote of what the ring no longer holds caught")
	}
	if !quotesOf(t, x, b[3800:3900]) {
		t.Fatal("a quote of what the ring holds not caught")
	}
}

// The guard judges any text: one below every threshold is clean, a nil
// guard or one with no index answers pending, a quote is output, and a
// JSON value is judged by each string it holds.
func TestOutputGuardOverAnyText(t *testing.T) {
	ctx := context.Background()
	r := rand.New(rand.NewPCG(11, 12))
	x := testIndex(t, "", 0)
	out := words(r, 2000)
	x.feed("ring:a", []byte(out), 1<<20)
	g := &outputGuard{index: x}
	if v := g.Check(ctx, "exit status 1"); v != verdictClean {
		t.Fatalf("a short phrase: %s", v)
	}
	if v := (*outputGuard)(nil).Check(ctx, "a sentence long enough to be judged"); v != verdictPending {
		t.Fatalf("no guard: %s", v)
	}
	if v := (&outputGuard{}).Check(ctx, "a sentence long enough to be judged"); v != verdictPending {
		t.Fatalf("no index: %s", v)
	}
	if v := g.Check(ctx, "the log says "+out[300:400]); v != verdictOutput {
		t.Fatalf("a quote: %s", v)
	}
	if v := g.Check(ctx, "a sentence of a person's own, long enough to be judged"); v != verdictClean {
		t.Fatalf("own words: %s", v)
	}
	value := `{"id":"t1","title":"Fix it","description":"` + strings.ReplaceAll(out[500:620], " ", `\n`) + `"}`
	if v := g.JudgeValue(ctx, value, false); v != verdictOutput {
		t.Fatalf("a create's field quoting output: %s", v)
	}
	if v := g.JudgeValue(ctx, `{"id":"t1","title":"Fix the flaky widget test"}`, true); v != verdictClean {
		t.Fatalf("an agent's own title under the captured-bytes reading: %s", v)
	}
}

// An export leaves the index out as derived: it holds fingerprints of what
// this server captured, made again from the sources where it is absent.
func TestOutputIndexLeftOutOfTheExport(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	x := e.srv.outputs
	x.feed("ring:s1", []byte(strings.Repeat("captured output of a session ", 10)), 1<<20)
	if !x.whole(context.Background()) {
		t.Fatal("not whole")
	}
	if _, err := os.Stat(filepath.Join(e.srv.store.Dir(), indexFile)); err != nil {
		t.Fatal(err)
	}
	resp, b := getExport(t, e, adminToken)
	if resp.StatusCode != 200 {
		t.Fatalf("the export: %d %.200s", resp.StatusCode, b)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if strings.Contains(f.Name, indexFile) {
			t.Fatalf("the archive holds %s", f.Name)
		}
		if f.Name == export.ManifestName {
			rc, _ := f.Open()
			var m export.Manifest
			_ = json.NewDecoder(rc).Decode(&m)
			rc.Close()
			if !slices.Contains(m.Left, export.Left{Path: indexFile, Why: export.WhyDerived}) {
				t.Fatalf("the manifest's left out: %+v", m.Left)
			}
		}
	}
}

// A log of one character repeated keeps one fingerprint a run of windows,
// not one a byte: a megabyte of it is a few tens of thousands of rows in
// the index, and a quote of a passage inside it is still caught.
func TestARepeatedLogKeepsOneFingerprintARun(t *testing.T) {
	x := testIndex(t, "", 0)
	log := strings.Repeat("x", 1<<20) + " the end of a long run of the same character, then words " + strings.Repeat("y", 1<<16)
	x.keepText("check:big", []byte(log))
	x.reader("check", func(id string) ([]byte, bool) { return []byte(log), id == "big" })
	if !quotesOf(t, x, "it said: "+log[(1<<20)-40:(1<<20)+60]) {
		t.Fatal("a quote across the run's end not caught")
	}
	var n int
	if err := x.db.QueryRow("SELECT COUNT(*) FROM fp WHERE source = 'check:big'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n > len(log)/indexRun+100 {
		t.Fatalf("%d fingerprints for %d bytes", n, len(log))
	}
}
