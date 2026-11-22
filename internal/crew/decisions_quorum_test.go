package crew

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The places a router's policy's counted approvals fill (SW1 PR-9,
// decisions_quorum.go), decided from the rows that stand now: the hard
// cases of the matching, and what each verdict of an approval is.

// quorumPolicy is a policy of groups (role:quorum:eligible subjects...),
// distinct as given.
func quorumPolicy(distinct bool, groups ...[]string) routedPolicy {
	p := routedPolicy{Revision: "r1", Distinct: distinct}
	for _, g := range groups {
		var q int
		if _, err := fmt.Sscan(g[1], &q); err != nil {
			panic(err)
		}
		req := ext.Requirement{Role: g[0], Label: g[0] + " owner", Quorum: q, Rule: "r-1"}
		for _, s := range g[2:] {
			req.Eligible = append(req.Eligible, ext.Eligible{Subject: s, Name: s, Via: ext.ViaRole, Ref: "ref-" + s})
		}
		p.Groups = append(p.Groups, req)
	}
	return p
}

// approvalsOf are counted rows, each a subject confirmed in every role
// given ("" for every group of p).
func approvalsOf(p routedPolicy, subjects ...string) []countedRow {
	var roles []string
	for _, g := range p.Groups {
		roles = append(roles, g.Role)
	}
	var out []countedRow
	for i, s := range subjects {
		out = append(out, countedRow{k: i + 1, subject: s, groups: roles})
	}
	return out
}

func TestFillFindsTheMaximumMatching(t *testing.T) {
	cases := []struct {
		name   string
		p      routedPolicy
		rows   []countedRow
		filled int
		per    []int
	}{
		{
			// A greedy choice gives Ana the first group she is named in,
			// security, the one place Cam can fill: the matching moves her.
			name: "the greedy choice undone",
			p:    quorumPolicy(true, []string{"security", "1", "ana", "cam"}, []string{"architecture", "1", "ana"}),
			rows: nil, filled: 2, per: []int{1, 1},
		},
		{
			// A path of two moves: P3 takes A from P1, who takes C, past P2
			// holding B.
			name: "an augmenting path through two holders",
			p:    quorumPolicy(true, []string{"a", "1", "p1", "p2", "p3"}, []string{"b", "1", "p1", "p2"}, []string{"c", "1", "p1"}),
			rows: nil, filled: 3, per: []int{1, 1, 1},
		},
		{
			name: "a quorum of two beside one",
			p:    quorumPolicy(true, []string{"a", "2", "p1", "p2"}, []string{"b", "1", "p1", "p2", "p3"}),
			rows: nil, filled: 3, per: []int{2, 1},
		},
		{
			// A person eligible in every group fills one place with distinct
			// persons, one in each without.
			name: "one person in every group, distinct",
			p:    quorumPolicy(true, []string{"a", "1", "ana"}, []string{"b", "1", "ana"}, []string{"c", "1", "ana"}, []string{"d", "1", "ana"}),
			rows: nil, filled: 1,
		},
		{
			name: "one person in every group, not distinct",
			p:    quorumPolicy(false, []string{"a", "1", "ana"}, []string{"b", "1", "ana"}, []string{"c", "1", "ana"}, []string{"d", "1", "ana"}),
			rows: nil, filled: 4, per: []int{1, 1, 1, 1},
		},
	}
	// Each case's rows are every subject the policy names, in order, unless
	// given.
	for i := range cases {
		if cases[i].rows != nil {
			continue
		}
		var seen []string
		for _, g := range cases[i].p.Groups {
			for _, e := range g.Eligible {
				if !contains(seen, e.Subject) {
					seen = append(seen, e.Subject)
				}
			}
		}
		cases[i].rows = approvalsOf(cases[i].p, seen...)
	}
	for _, c := range cases {
		filled, per := fill(c.p, c.rows)
		if filled != c.filled {
			t.Errorf("%s: %d filled, want %d (%v)", c.name, filled, c.filled, per)
		}
		if c.per != nil && fmt.Sprint(per) != fmt.Sprint(c.per) {
			t.Errorf("%s: per group %v, want %v", c.name, per, c.per)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Four groups of quorum four, distinct persons, sixteen people each named
// in one group: all sixteen places filled; a seventeenth, named in every
// group, answers with one place left, and answers too once every place is
// filled already; a counted person again, while a place is left that
// they cannot fill, is already counted; a person eligible in every group
// alone fills one place.
func TestFourGroupsOfQuorumFour(t *testing.T) {
	var groups [][]string
	var all []string
	for g := range 4 {
		grp := []string{fmt.Sprintf("g%d", g), "4"}
		for i := range 4 {
			s := fmt.Sprintf("p%d%d", g, i)
			grp = append(grp, s)
			all = append(all, s)
		}
		groups = append(groups, append(grp, "every"))
	}
	p := quorumPolicy(true, groups...)
	if placesOf(p) != 16 {
		t.Fatalf("%d places", placesOf(p))
	}
	rows := approvalsOf(p, all...)
	if filled, per := fill(p, rows); filled != 16 || fmt.Sprint(per) != "[4 4 4 4]" {
		t.Fatalf("sixteen persons fill %d (%v)", filled, per)
	}
	if v, _ := judgeCount(p, rows[:15], "every", []string{"g0", "g1", "g2", "g3"}); v != countAnswers {
		t.Fatalf("the one eligible everywhere, with one place left: %d", v)
	}
	if v, _ := judgeCount(p, rows, "every", []string{"g0", "g1", "g2", "g3"}); v != countAnswers {
		t.Fatalf("the one eligible everywhere, every place filled already: %d", v)
	}
	if v, _ := judgeCount(p, rows[:15], "p00", []string{"g0"}); v != countAlready {
		t.Fatalf("a counted person again, one place left elsewhere: %d", v)
	}
	if v, _ := judgeCount(p, rows[1:], "p01", []string{"g0"}); v != countAlready {
		t.Fatalf("a counted person again, their group's place left to another: %d", v)
	}
	if filled, _ := fill(p, approvalsOf(p, "every")); filled != 1 {
		t.Fatalf("the one eligible everywhere alone fills %d", filled)
	}
}

// The places are decided from what stands now: a person counted only in
// the groups the policy names it in now and the router confirmed at its
// response; one approval per person whatever its rows; a re-route that
// takes the only person able to fill a place breaks the matching.
func TestFillReadsTheRowsAsTheyStandNow(t *testing.T) {
	p := quorumPolicy(true, []string{"a", "1", "p1"}, []string{"b", "1", "p1", "p2"}, []string{"c", "1", "p2", "p3"})
	rows := approvalsOf(p, "p1", "p2", "p3")
	if filled, _ := fill(p, rows); filled != 3 {
		t.Fatalf("before: %d", filled)
	}
	// p3 is no longer named in c: p1 takes a, p2 one of b and c, nobody the
	// other.
	revoked := quorumPolicy(true, []string{"a", "1", "p1"}, []string{"b", "1", "p1", "p2"}, []string{"c", "1", "p2"})
	if filled, _ := fill(revoked, rows); filled != 2 {
		t.Fatalf("after the revocation: %d", filled)
	}
	// Confirmed at their responses in b alone: p2 fills b, and p3, whom
	// the policy names in c alone, fills nothing (nor c, never confirmed).
	narrow := []countedRow{{k: 1, subject: "p2", groups: []string{"b"}}, {k: 2, subject: "p3", groups: []string{"b"}}}
	if filled, per := fill(p, narrow); filled != 1 || fmt.Sprint(per) != "[0 1 0]" {
		t.Fatalf("confirmed groups: %d %v", filled, per)
	}
	// The same subject twice is one person.
	twice := []countedRow{{k: 1, subject: "p2", groups: []string{"b"}}, {k: 2, subject: "p2", groups: []string{"c"}}}
	if filled, _ := fill(p, twice); filled != 1 {
		t.Fatalf("one person's two rows fill %d", filled)
	}
}

// The verdicts of an eligible approval: the one that fills the last place
// answers; one that fills a place and leaves one is counted; one that
// fills nothing still needed is not needed; a subject whose approval
// counts already is already counted.
func TestJudgeCountVerdicts(t *testing.T) {
	p := quorumPolicy(true, []string{"architecture", "1", "ana", "ben"}, []string{"security", "1", "cam"})
	if v, per := judgeCount(p, nil, "ana", []string{"architecture"}); v != countCounted || fmt.Sprint(per) != "[1 0]" {
		t.Fatalf("ana first: %d %v", v, per)
	}
	ana := []countedRow{{k: 1, subject: "ana", groups: []string{"architecture"}}}
	if v, _ := judgeCount(p, ana, "ana", []string{"architecture"}); v != countAlready {
		t.Fatalf("ana again: %d", v)
	}
	if v, per := judgeCount(p, ana, "ben", []string{"architecture"}); v != countNotNeeded || p.waitsForWords(per) != "it waits for the security owner" {
		t.Fatalf("ben once architecture is met: %d %q", v, p.waitsForWords(per))
	}
	if v, _ := judgeCount(p, ana, "cam", []string{"security"}); v != countAnswers {
		t.Fatalf("cam, the last place: %d", v)
	}
	if w := p.notNeededWords("dr-7", []int{0}, []int{1, 1}); w != "dr-7 has the architecture owner's approvals it needs" {
		t.Fatalf("the words with nothing to wait for: %q", w)
	}
}

// With every place filled by the approvals that stand (a re-route brought
// back a counted approver), any eligible approval answers the decision,
// a counted approver's too, whatever place it would fill; a person whose
// standing approval fills nothing after a re-route moved them is judged
// anew, counted where their new group needs them, and only then already
// counted.
func TestJudgeCountAfterAReroute(t *testing.T) {
	back := quorumPolicy(true, []string{"architecture", "1", "ana", "ben"}, []string{"security", "1", "cam"})
	full := []countedRow{{k: 1, subject: "ana", groups: []string{"architecture"}}, {k: 2, subject: "cam", groups: []string{"security"}}}
	for _, who := range []string{"ben", "ana", "cam"} {
		if v, _ := judgeCount(back, full, who, back.confirmedFor(who)); v != countAnswers {
			t.Fatalf("%s with every place filled: %d", who, v)
		}
	}
	moved := quorumPolicy(true, []string{"architecture", "1", "ben"}, []string{"security", "1", "ana"})
	stale := []countedRow{{k: 1, subject: "ana", groups: []string{"architecture"}}}
	if v, per := judgeCount(moved, stale, "ana", []string{"security"}); v != countCounted || fmt.Sprint(per) != "[0 1]" {
		t.Fatalf("ana moved to security: %d %v", v, per)
	}
	again := append(slices.Clone(stale), countedRow{k: 2, subject: "ana", groups: []string{"security"}})
	if v, _ := judgeCount(moved, again, "ana", []string{"security"}); v != countAlready {
		t.Fatalf("ana's third approval while architecture waits: %d", v)
	}
	if v, _ := judgeCount(moved, again, "ben", []string{"architecture"}); v != countAnswers {
		t.Fatalf("ben, the last place: %d", v)
	}
}

// confirmedFor are the roles p names subject in: what a router holding p
// confirms.
func (p routedPolicy) confirmedFor(subject string) []string {
	var out []string
	for _, g := range p.Groups {
		if slices.ContainsFunc(g.Eligible, func(e ext.Eligible) bool { return e.Subject == subject }) {
			out = append(out, g.Role)
		}
	}
	return out
}

// bruteFill is the places rows fill under p, found by trying every
// assignment of persons to groups (one place a person under distinct
// persons): a check of fill that shares nothing with it.
func bruteFill(p routedPolicy, rows []countedRow) int {
	groupsOf := map[string][]int{}
	var people []string
	for _, r := range rows {
		if _, seen := groupsOf[r.subject]; !seen {
			people = append(people, r.subject)
			groupsOf[r.subject] = nil
		}
		for i, g := range p.Groups {
			named := slices.ContainsFunc(g.Eligible, func(e ext.Eligible) bool { return e.Subject == r.subject })
			if named && slices.Contains(r.groups, g.Role) && !slices.Contains(groupsOf[r.subject], i) {
				groupsOf[r.subject] = append(groupsOf[r.subject], i)
			}
		}
	}
	if !p.Distinct {
		total := 0
		for i, g := range p.Groups {
			n := 0
			for _, who := range people {
				if slices.Contains(groupsOf[who], i) {
					n++
				}
			}
			total += min(n, g.Quorum)
		}
		return total
	}
	used := make([]int, len(p.Groups))
	var best func(i int) int
	best = func(i int) int {
		if i == len(people) {
			return 0
		}
		top := best(i + 1) // this person fills nothing
		for _, g := range groupsOf[people[i]] {
			if used[g] < p.Groups[g].Quorum {
				used[g]++
				top = max(top, 1+best(i+1))
				used[g]--
			}
		}
		return top
	}
	return best(0)
}

// A counted row stands when its recheck admitted it; one this process
// accepted stands while its recheck is pending; one rechecked and refused,
// one of another process never rechecked, and one whose wait ended with no
// row stand not.
func TestCountedRowsStandByTheirRecheck(t *testing.T) {
	at := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	row := func(k int, subject, boot string) ledger.ResponseRow {
		return ledger.ResponseRow{K: k, Actor: "user:" + subject, Status: ledger.ResponseCounted, At: at,
			Authority: fmt.Sprintf(`{"grant":"decide","via":"identity","subject":%q,"boot":%q}`, subject, boot),
			Receipt:   `{"status":"counted","groups":["architecture"]}`}
	}
	rec := ledger.DecisionRecord{DecisionRow: ledger.DecisionRow{N: 7}, Responses: []ledger.ResponseRow{
		row(1, "ana", "old"), row(2, "ben", "old"), row(3, "cam", "now"), row(4, "dan", "now"), row(5, "eve", "old"),
	}, History: []ledger.HistoryRow{
		{Kind: ledger.HistoryRechecked, Detail: `{"k":1,"admitted":true}`},
		{Kind: ledger.HistoryRechecked, Detail: `{"k":2,"admitted":false}`},
	}}
	pending := func(k int) bool { return k == 3 }
	standing, void := countedRows(rec, "now", pending)
	var stood, fell []string
	for _, c := range standing {
		stood = append(stood, c.subject)
	}
	for _, c := range void {
		fell = append(fell, c.subject)
	}
	if fmt.Sprint(stood) != "[ana cam]" || fmt.Sprint(fell) != "[ben dan eve]" {
		t.Fatalf("standing %v, void %v", stood, fell)
	}
	if !standing[0].confirmed || standing[1].confirmed {
		t.Fatalf("confirmed: %+v", standing)
	}
}
