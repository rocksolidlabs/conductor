package brakeman

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// aboutRow is a row of testdata/about-table.json, the one table the
// server's about rule and the app's subject half are both held to.
type aboutRow struct {
	About       string      `json:"about"`
	Subjects    []string    `json:"subjects"`
	DigestAbout DigestAbout `json:"digestAbout"`
	Match       string      `json:"match"`
}

func aboutTable(t *testing.T) []aboutRow {
	t.Helper()
	b, err := os.ReadFile("testdata/about-table.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []aboutRow
	if err := store.DecodeStrict(b, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 20 {
		t.Fatalf("the about table holds %d rows", len(rows))
	}
	return rows
}

// The about rule is the table's, row by row: a subject naming the thing
// or a part of it matches as subject, and wins over a citation; the
// digest's about matches as cited; nothing else matches. Each about kind
// has rows of all three answers.
func TestAboutMatchesTheTable(t *testing.T) {
	seen := map[string]map[string]bool{}
	for i, row := range aboutTable(t) {
		about, err := ParseAbout(row.About)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		var subjects []Ref
		for _, s := range row.Subjects {
			r, err := ParseRef(s)
			if err != nil {
				t.Fatalf("row %d: %v", i, err)
			}
			subjects = append(subjects, r)
		}
		d := &Digest{Schema: DigestSchema, About: row.DigestAbout}
		if got := AboutMatch(about, subjects, d); got != row.Match {
			t.Errorf("row %d (%s, %v, %+v): %q, want %q", i, row.About, row.Subjects, row.DigestAbout, got, row.Match)
		}
		if row.Match != MatchCited {
			// With no digest a row matches as it does with an empty one,
			// unless the citation was its match.
			if got := AboutMatch(about, subjects, nil); got != row.Match {
				t.Errorf("row %d with no digest: %q, want %q", i, got, row.Match)
			}
		}
		if seen[about.Kind] == nil {
			seen[about.Kind] = map[string]bool{}
		}
		seen[about.Kind][row.Match] = true
	}
	for _, k := range AboutKinds {
		for _, m := range []string{MatchSubject, MatchCited, ""} {
			if !seen[k][m] {
				t.Errorf("no row of about kind %s answers %q", k, m)
			}
		}
	}
}

// An about filter takes a run, a task, a session or a ticket: another
// kind, a ref that does not parse, one past MaxRef and an empty one are
// refused.
func TestParseAboutRefusesOtherRefKinds(t *testing.T) {
	for _, s := range []string{
		"run:" + runA, "task:" + runA + "/build", "session:" + sessA, "ticket:CND-12",
	} {
		if _, err := ParseAbout(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{
		"", "comment:CND-12/" + commentA, "check:" + runA + "/build/2/vet/1", "attempt:" + runA + "/build/1",
		"question:" + sessA + "/" + questA, "run:", "run:Nope", "nonsense",
		"ticket:CND-" + strings.Repeat("1", MaxRef),
	} {
		_, err := ParseAbout(s)
		var re *RefError
		if !errors.As(err, &re) {
			t.Errorf("%.40s: %v, want a RefError", s, err)
		}
	}
}
