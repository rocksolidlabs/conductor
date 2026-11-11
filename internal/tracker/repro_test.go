package tracker

import (
	"strings"
	"testing"
)

func TestRepro(t *testing.T) {
	cases := []struct {
		name, body, want string
		ok               bool
	}{
		{"issue form", "### Describe\r\n\r\nIt breaks.\r\n\r\n### Steps to reproduce\r\n\r\n1. Open\r\n2. Click\r\n\r\n### Expected\r\n\r\nNothing.", "1. Open\n2. Click", true},
		{"level one to the end", "# Repro\n\nrun it\n", "run it", true},
		{"level two, punctuation and case", "## **Steps To Reproduce:**\nA\n### Detail\nB\n## Next\nC", "A\n### Detail\nB", true},
		{"level four", "#### How to reproduce\nstep", "step", true},
		{"closing hashes", "## Reproduction steps ##\nstep\n# Top\nx", "step", true},
		{"to reproduce", "### To reproduce\nstep", "step", true},
		{"reproduction", "### Reproduction\nstep", "step", true},
		{"repro steps", "### Repro steps\nstep", "step", true},
		{"level five is not a section", "##### Steps to reproduce\nstep", "", false},
		{"a heading in a fence is not one", "```\n## Repro\n```\n## Other\nx", "", false},
		{"a fence inside the section", "## Repro\n```sh\n# not a heading\nmake\n```\n## End", "```sh\n# not a heading\nmake\n```", true},
		{"the first heading wins", "## Repro\nfirst\n## Repro\nsecond", "first", true},
		{"no space is not a heading", "##Repro\nstep", "", false},
		{"other heading", "## Steps\nstep", "", false},
		{"empty section", "## Repro\n\n## Next\nx", "", false},
		{"none", "just text", "", false},
	}
	for _, c := range cases {
		got, ok := Repro(c.body)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Repro = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestReproCutAtALineEnd(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	body := "## Repro\n" + strings.Repeat(line, 200)
	got, ok := Repro(body)
	if !ok || len(got) > MaxRepro || !strings.HasSuffix(got, "\n…") {
		t.Fatalf("cut: %d bytes, ok %v, suffix %q", len(got), ok, got[len(got)-8:])
	}
	kept := strings.TrimSuffix(got, "\n…")
	for _, l := range strings.Split(kept, "\n") {
		if len(l) != 99 {
			t.Fatalf("a line cut in its middle: %d bytes", len(l))
		}
	}
	// One line longer than the bound is cut at a rune boundary.
	got, _ = Repro("## Repro\n" + strings.Repeat("é", MaxRepro))
	if len(got) > MaxRepro || !strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "é") {
		t.Fatalf("one long line: %d bytes", len(got))
	}
	// At the bound exactly, nothing is cut.
	exact := strings.Repeat("y", MaxRepro)
	if got, _ := Repro("## Repro\n" + exact); got != exact {
		t.Fatalf("at the bound: %d bytes", len(got))
	}
}
