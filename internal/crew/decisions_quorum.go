package crew

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Groups, quorum and distinct persons on a decision record (SW1 PR-9,
// docs/plans/decision-policy.md). A router's policy of several groups, or
// of a quorum above one, has several places: the sum of its groups'
// quorums. An eligible approval that fills a place and leaves one unfilled
// is kept counted (ledger.ResponseCounted), the decision staying open; the
// approval that fills the last place is accepted and answers it. Which
// places the counted approvals fill is decided from the rows that stand
// now, every time it is asked (at each response, at a re-route, at the
// application), never kept: each counted subject in the groups the policy
// names it in now and the router confirmed at its response; without
// distinct persons one person may fill a place in several groups, with
// them the largest assignment of persons to places, one place each
// (fill). A counted approval stands while its sign-in has been rechecked
// and admitted (a rechecked row), or, in the process that accepted it,
// while that recheck is still to answer (pending); one rechecked and not
// admitted, or found unrechecked after a restart, stands not.

// countedRow is one counted approval of a record as the record keeps it.
type countedRow struct {
	k       int
	subject string
	// groups are the roles the router confirmed the subject eligible in at
	// its response (its receipt's).
	groups    []string
	at        time.Time
	name      string
	confirmed bool // rechecked and admitted
}

// countedRows are rec's counted approvals that stand for the process boot
// (standing), and those that stand not (void): a counted row with a
// rechecked row admitting it stands; one with a rechecked row refusing it
// stands not; one with none stands only when this process accepted it and
// its recheck is still to answer (pending says so of its k).
func countedRows(rec ledger.DecisionRecord, boot string, pending func(k int) bool) (standing, void []countedRow) {
	rechecked := map[int]bool{}
	for _, h := range rec.History {
		if h.Kind != ledger.HistoryRechecked {
			continue
		}
		var dt struct {
			K        int  `json:"k"`
			Admitted bool `json:"admitted"`
		}
		if json.Unmarshal([]byte(h.Detail), &dt) != nil {
			continue
		}
		if _, seen := rechecked[dt.K]; !seen {
			rechecked[dt.K] = dt.Admitted
		}
	}
	for _, r := range rec.Responses {
		if r.Status != ledger.ResponseCounted {
			continue
		}
		var auth Authority
		var rc ResponseReceipt
		if json.Unmarshal([]byte(r.Authority), &auth) != nil || json.Unmarshal([]byte(r.Receipt), &rc) != nil || auth.Subject == "" || auth.Via != ViaIdentity {
			// A counted row whose authority cannot be read is no approval.
			void = append(void, countedRow{k: r.K, at: r.At})
			continue
		}
		c := countedRow{k: r.K, subject: auth.Subject, groups: slices.Clone(rc.Groups), at: r.At, name: nameOf(rc.ByName, r.Actor)}
		admitted, known := rechecked[r.K]
		switch {
		case known && admitted:
			c.confirmed = true
			standing = append(standing, c)
		case !known && auth.Boot == boot && pending != nil && pending(r.K):
			standing = append(standing, c)
		default:
			void = append(void, c)
		}
	}
	return standing, void
}

// placesOf is the places p's groups ask for in all.
func placesOf(p routedPolicy) int { return placesIn(p.Groups) }

// placesIn is the places groups ask for in all.
func placesIn(groups []ext.Requirement) int {
	n := 0
	for _, g := range groups {
		n += g.Quorum
	}
	return n
}

// counts says whether p counts approvals: more than one place.
func (p routedPolicy) counts() bool { return placesOf(p) > 1 }

// namedIn are the indexes of p's groups that name subject among their
// eligible now and that are among confirmed (the roles the router
// confirmed it in), in order.
func (p routedPolicy) namedIn(subject string, confirmed []string) []int {
	var out []int
	for i, g := range p.Groups {
		if !slices.Contains(confirmed, g.Role) {
			continue
		}
		if slices.ContainsFunc(g.Eligible, func(e ext.Eligible) bool { return e.Subject == subject }) {
			out = append(out, i)
		}
	}
	return out
}

// fill is the places the counted approvals rows fill under p: in all, and
// per group in p's order. Each subject counts once, as themselves (a
// delegate too), in the groups p names it in now among those confirmed at
// its responses. Without distinct persons a group's places filled are its
// quorum or its eligible approvers, the smaller; with them, the largest
// assignment of approvers to places, one place each (a maximum matching,
// found by augmenting paths: a greedy choice that takes a place another
// approver alone could fill is undone).
func fill(p routedPolicy, rows []countedRow) (int, []int) {
	type person struct {
		subject string
		groups  []int
	}
	var people []person
	for _, r := range rows {
		named := p.namedIn(r.subject, r.groups)
		if i := slices.IndexFunc(people, func(q person) bool { return q.subject == r.subject }); i >= 0 {
			for _, g := range named {
				if !slices.Contains(people[i].groups, g) {
					people[i].groups = append(people[i].groups, g)
				}
			}
			slices.Sort(people[i].groups)
			continue
		}
		people = append(people, person{subject: r.subject, groups: named})
	}
	per := make([]int, len(p.Groups))
	if !p.Distinct {
		for i, g := range p.Groups {
			for _, q := range people {
				if slices.Contains(q.groups, i) {
					per[i]++
				}
			}
			per[i] = min(per[i], g.Quorum)
		}
	} else {
		holders := make([][]int, len(p.Groups)) // the people each group's places hold
		var augment func(who int, seen []bool) bool
		augment = func(who int, seen []bool) bool {
			for _, g := range people[who].groups {
				if seen[g] {
					continue
				}
				seen[g] = true
				if len(holders[g]) < p.Groups[g].Quorum {
					holders[g] = append(holders[g], who)
					return true
				}
				for j, other := range holders[g] {
					if augment(other, seen) {
						holders[g][j] = who
						return true
					}
				}
			}
			return false
		}
		for who := range people {
			augment(who, make([]bool, len(p.Groups)))
		}
		for i := range holders {
			per[i] = len(holders[i])
		}
	}
	total := 0
	for _, n := range per {
		total += n
	}
	return total, per
}

// unmet is the places p still needs of rows: its places less those filled.
func unmet(p routedPolicy, rows []countedRow) int {
	filled, _ := fill(p, rows)
	return placesOf(p) - filled
}

// rowsShort says rec's response rows no longer leave a row for each place
// its policy p still needs of the counted approvals that stand: the rows
// left (ledger.MaxDecisionResponses less those kept) fewer than the places
// unmet. Its decision is then superseded (supersedeShort).
func rowsShort(rec ledger.DecisionRecord, p routedPolicy, standing []countedRow) bool {
	return ledger.MaxDecisionResponses-len(rec.Responses) < unmet(p, standing)
}

// waitsForWords say which groups p still needs, by their labels, after
// per: "it waits for the security owner", or "" when none.
func (p routedPolicy) waitsForWords(per []int) string {
	var labels []string
	for i, g := range p.Groups {
		if per[i] < g.Quorum {
			labels = append(labels, "the "+g.Label)
		}
	}
	if len(labels) == 0 {
		return ""
	}
	return "it waits for " + strings.Join(labels, " and ")
}

// metWords say which of groups (indexes into p) are met after per, by
// their labels: "the architecture owner's".
func (p routedPolicy) metWords(groups []int, per []int) string {
	var labels []string
	for _, i := range groups {
		if per[i] >= p.Groups[i].Quorum {
			labels = append(labels, "the "+p.Groups[i].Label+"'s")
		}
	}
	if len(labels) == 0 {
		return "the"
	}
	return strings.Join(labels, " and ")
}

// notNeededWords say why an approval filled no place: the groups it
// could fill that are met, and what the record still waits for, by
// labels, never by people; no clause is left empty.
func (p routedPolicy) notNeededWords(id string, groups, per []int) string {
	w := id + " has " + p.metWords(groups, per) + " approvals it needs"
	if waits := p.waitsForWords(per); waits != "" {
		w += ": " + waits
	}
	return w
}

// countVerdict is an eligible approval's judgement under a policy that
// counts (judgeCount).
type countVerdict int

const (
	countAnswers   countVerdict = iota // every place is filled with it: accepted
	countCounted                       // it fills a place more and leaves one: counted
	countNotNeeded                     // it fills no place more
	countAlready                       // it fills no place more, and its person's approval stands
)

// judgeCount judges subject's eligible approval, confirmed now in groups,
// under p, beside the counted approvals that stand: with its row added
// (its groups merged with its person's standing row's, one place a person
// under distinct persons), it answers the decision when every place is
// then filled, whether it filled the last place or the last was filled
// already (a re-route can bring back a counted approver); else it is
// counted when it fills a place more, or when a place is still unmet and
// it names its person, whose approval stands, in a group their standing
// approvals do not (it widens where they may be placed: under distinct
// persons that can be what lets another complete it); else it is refused,
// already counted for a person whose own approval stands and not needed
// otherwise. A person whose standing approval fills nothing (a re-route
// moved them) is so judged anew. The places filled after it, per group, are returned.
func judgeCount(p routedPolicy, standing []countedRow, subject string, groups []string) (countVerdict, []int) {
	before, per := fill(p, standing)
	after, perAfter := fill(p, append(slices.Clone(standing), countedRow{subject: subject, groups: groups}))
	switch {
	case after >= placesOf(p):
		return countAnswers, perAfter
	case after > before:
		return countCounted, perAfter
	case slices.ContainsFunc(standing, func(c countedRow) bool { return c.subject == subject }):
		if before < placesOf(p) && p.widens(standing, subject, groups) {
			// It fills no place more now, but it names a group the
			// person's standing approvals do not (a re-route named them
			// there): counted, so the person may be placed there once
			// another fills the place they hold now.
			return countCounted, per
		}
		return countAlready, per
	}
	return countNotNeeded, per
}

// widens says groups name subject in a group of p that none of subject's
// standing approvals fills for them now (namedIn over each row).
func (p routedPolicy) widens(standing []countedRow, subject string, groups []string) bool {
	var held []int
	for _, c := range standing {
		if c.subject == subject {
			held = append(held, p.namedIn(subject, c.groups)...)
		}
	}
	for _, g := range p.namedIn(subject, groups) {
		if !slices.Contains(held, g) {
			return true
		}
	}
	return false
}

// QuorumOf is the operator's count of rec's places under its router's
// policy (the read's quorum): the places in all, those the counted
// approvals that stand for the process boot fill now, and each group's
// count by its role; nil for a record under the server's own policy.
func QuorumOf(rec ledger.DecisionRecord, boot string, pending func(n int64, k int) bool) *proto.DecisionQuorum {
	p, ok := routedOf(rec.Policy)
	if !ok {
		return nil
	}
	standing, _ := countedRows(rec, boot, func(k int) bool { return pending != nil && pending(rec.N, k) })
	if c, ok := decidingApproval(rec); ok {
		// The approval that answered fills its places too.
		standing = append(standing, c)
	}
	filled, per := fill(p, standing)
	q := &proto.DecisionQuorum{Places: placesOf(p), Filled: filled, Groups: []proto.QuorumGroup{}}
	for i, g := range p.Groups {
		q.Groups = append(q.Groups, proto.QuorumGroup{Role: g.Role, Quorum: g.Quorum, Counted: per[i], Met: per[i] >= g.Quorum})
	}
	return q
}

// countedWords are the words of a counted approval's receipt: whose, for
// which groups, how many places of how many, and what it waits for.
func countedWords(id, by string, p routedPolicy, groups []string, per []int) string {
	filled := 0
	for _, n := range per {
		filled += n
	}
	w := fmt.Sprintf("Counted: %s approved by %s (eligible for %s): %d of %d", id, by, strings.Join(groups, ", "), filled, placesOf(p))
	if waits := p.waitsForWords(per); waits != "" {
		w += "; " + waits
	}
	return w
}

// voidedReceipt is counted receipt rc of rec as it reads once its approval
// no longer counts: refused, not_confirmed, the server's words, which ask
// for another answer only while the decision is open.
func voidedReceipt(rec ledger.DecisionRecord, rc ResponseReceipt) ResponseReceipt {
	rc.Status, rc.Code = ledger.ResponseRefused, CodeNotConfirmed
	rc.Words = fmt.Sprintf("%s's approval of %s no longer counts: its sign-in could not be rechecked", nameOf(rc.ByName, rc.Actor), rec.ID())
	if rec.State == ledger.DecisionOpen {
		rc.Words += ": answer again"
	}
	return rc
}

// decidingApproval is rec's accepted response as a place-filling row,
// ok when it is an eligible approval: an approval's or an assignment's
// first option with its groups, never an override, a Reject or an answer
// under the server's own policy (they fill no place).
func decidingApproval(rec ledger.DecisionRecord) (countedRow, bool) {
	for _, r := range rec.Responses {
		if r.Status != ledger.ResponseAccepted {
			continue
		}
		var auth Authority
		var rc ResponseReceipt
		if json.Unmarshal([]byte(r.Authority), &auth) != nil || json.Unmarshal([]byte(r.Receipt), &rc) != nil {
			return countedRow{}, false
		}
		if rc.Override || len(rc.Groups) == 0 || auth.Subject == "" || !approvesRecord(rec.Type, Response{Choice: rc.Choice}) {
			return countedRow{}, false
		}
		return countedRow{k: r.K, subject: auth.Subject, groups: slices.Clone(rc.Groups), at: r.At, name: nameOf(rc.ByName, r.Actor)}, true
	}
	return countedRow{}, false
}

// CountedStands is whether each counted approval of rec still counts for
// the process boot (countedRows), by its k, for what a decide principal
// reads of its own: pending says whether decision n's counted response k
// waits for its recheck here.
func CountedStands(rec ledger.DecisionRecord, boot string, pending func(n int64, k int) bool) func(k int) bool {
	standing, _ := countedRows(rec, boot, func(k int) bool { return pending != nil && pending(rec.N, k) })
	return func(k int) bool {
		return slices.ContainsFunc(standing, func(c countedRow) bool { return c.k == k })
	}
}

// CountedOf are rec's counted approvals that stand for the process boot,
// as a router reads them (ext.OpenDecision.Counted): each subject, the
// groups the router confirmed it in at its response, its time, and
// whether its sign-in's recheck admitted it. None under the server's own
// policy.
func CountedOf(rec ledger.DecisionRecord, boot string, pending func(n int64, k int) bool) []ext.CountedResponse {
	if !ledger.RouterPolicy(rec.Policy) {
		return nil
	}
	standing, _ := countedRows(rec, boot, func(k int) bool { return pending != nil && pending(rec.N, k) })
	var out []ext.CountedResponse
	for _, c := range standing {
		out = append(out, ext.CountedResponse{Subject: c.subject, Groups: slices.Clone(c.groups), At: c.at, Confirmed: c.confirmed})
	}
	return out
}
