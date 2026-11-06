package history

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"
)

// benchVocabulary is made-up words, the same every run, two to four
// syllables each (Dispatch's benchmark's way, BenchmarkList).
func benchVocabulary(r *rand.Rand, n int) []string {
	syllables := []string{"ka", "lo", "mi", "ne", "ro", "ta", "vu", "se", "di", "po", "ga", "ru", "fe", "zo", "bi", "chu", "an", "el", "or", "ix"}
	seen := map[string]bool{}
	var words []string
	for len(words) < n {
		var b strings.Builder
		for range 2 + r.Intn(3) {
			b.WriteString(syllables[r.Intn(len(syllables))])
		}
		if w := b.String(); !seen[w] {
			seen[w] = true
			words = append(words, w)
		}
	}
	return words
}

// benchIndex is the benchmark's index: 100,000 entries of
// mixed kinds in 200 sources of 500, as the indexer would write them, a
// source at a time. Of each source's entries, a fifth are check receipts
// (half failed) with a log of 200 to 600 words, a tenth decisions, a
// tenth records, the rest said (a session's lines, 10 to 60 words), in
// three projects and two hosts, over a year. Words are drawn by a Zipf
// law, so a few are everywhere and most are rare.
func benchIndex(dir string) (*benchFixture, error) {
	ctx := context.Background()
	ix, err := Open(ctx, dir+"/"+File, Options{})
	if err != nil {
		return nil, err
	}
	r := rand.New(rand.NewSource(1))
	words := benchVocabulary(r, 4000)
	z := rand.NewZipf(r, 1.07, 2, uint64(len(words)-1))
	text := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = words[z.Uint64()]
		}
		return strings.Join(parts, " ")
	}
	projects := []string{"BENA", "BENB", "BENC"}
	base := time.Date(2025, 11, 5, 0, 0, 0, 0, time.UTC)
	var phrase string
	for s := range 200 {
		src := fmt.Sprintf("run:bench-%03d", s)
		entries := make([]Entry, 0, 500)
		for i := range 500 {
			n := s*500 + i
			at := base.Add(time.Duration(n) * 5 * time.Minute)
			e := Entry{Key: fmt.Sprintf("%s#%d", src, i), Ref: src, Anchor: src, At: at, Project: projects[n%3],
				Host: []string{"lab-box", "mac"}[n%2], Run: fmt.Sprintf("bench-%03d", s), Repo: "/code/" + projects[n%3] + "/.git"}
			switch k := i % 10; {
			case k < 2:
				e.Kind, e.Commit, e.Subject = "check", "0123456789abcdef0123456789abcdef01234567", fmt.Sprintf("subject-%d", i%7)
				e.Outcome = []string{"passed", "failed"}[i%2]
				e.Title, e.Summary, e.Body = "unit on build #"+fmt.Sprint(i), "go test ./...", text(200+r.Intn(400))
			case k == 2:
				e.Kind, e.Actor, e.Title, e.Body = "command", "admin", "retry", text(10)
			case k == 3:
				e.Kind, e.Title, e.Body = "attempt", "build #"+fmt.Sprint(i), text(20)
			default:
				e.Kind, e.Title, e.Body = "session.chat", "chat", text(10+r.Intn(50))
			}
			if phrase == "" && e.Kind == "session.chat" {
				phrase = strings.Join(strings.Fields(e.Body)[3:5], " ")
			}
			entries = append(entries, e)
		}
		if err := ix.Replace(ctx, Source{Key: src, Mark: "m"}, entries, nil); err != nil {
			return nil, err
		}
	}
	return &benchFixture{ix: ix, words: words, phrase: phrase}, nil
}

type benchFixture struct {
	ix     *Index
	words  []string
	phrase string
}

// BenchmarkSearch searches 100,000 entries (docs/plans/history.md,
// Measured): the plan's four (two words, a page of filters, a phrase, a
// prefix of two letters) and the searches that match nearly every entry
// (a prefix of one letter, the fixture's most frequent word alone, with a
// second word, and with filters), which reach the search's bounds
// (MaxScanned, MaxRanked); each reports its median, the plan's measure
// (target 50 ms on the Linux box), beside Go's mean.
func BenchmarkSearch(b *testing.B) {
	// A benchmark with sub-benchmarks runs once: the fixture is made once.
	f, err := benchIndex(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer f.ix.Close()
	for _, c := range []struct{ name, line string }{
		{"two-words", f.words[40] + " " + f.words[300]},
		{"filtered", "class:checked outcome:failed project:BENA"},
		{"phrase", `"` + f.phrase + `"`},
		{"prefix-2", "ka"},
		{"prefix-1", "k"},
		{"common-word", f.words[0]},
		{"common-two-words", f.words[0] + " " + f.words[1]},
		{"common-word-filtered", f.words[0] + " class:checked project:BENA"},
	} {
		b.Run(c.name, func(b *testing.B) {
			q, err := ParseQuery(c.line)
			if err != nil {
				b.Fatal(err)
			}
			var took []time.Duration
			var total int
			var cut string
			for b.Loop() {
				began := time.Now()
				p, err := f.ix.Search(context.Background(), q)
				if err != nil {
					b.Fatal(err)
				}
				took = append(took, time.Since(began))
				total, cut = p.Total, p.Cut
			}
			slices.Sort(took)
			b.ReportMetric(float64(took[len(took)/2].Microseconds())/1000, "ms-median")
			b.ReportMetric(float64(total), "matches")
			if cut != "" {
				b.Logf("%s: cut at %s", c.name, cut)
			}
		})
	}
}
