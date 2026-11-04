package crew

import "testing"

// A review's findings are counted in words: one is "1 finding", any other
// count "N findings" (#591).
func TestFindingsWords(t *testing.T) {
	for n, want := range map[int]string{0: "0 findings", 1: "1 finding", 2: "2 findings", 12: "12 findings"} {
		if got := findingsWords(n); got != want {
			t.Errorf("findingsWords(%d) = %q, want %q", n, got, want)
		}
	}
	one := Result{Verdict: VerdictNeedsChanges, Findings: []Finding{{Severity: "major", Path: "a.txt", Line: 1, Message: "say why"}}}
	if got := resultWords(TaskReview, "", one); got != "the verdict needs_changes with 1 finding" {
		t.Errorf("one finding: %q", got)
	}
	two := Result{Verdict: VerdictApproved, Findings: []Finding{{Message: "a"}, {Message: "b"}}}
	if got := resultWords(TaskReview, "", two); got != "the verdict approved with 2 findings" {
		t.Errorf("two findings: %q", got)
	}
}
