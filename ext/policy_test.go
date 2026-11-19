package ext_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// samplePolicyRecord and samplePolicy are a record and a policy valid for
// it, which each case below changes in one place.
func samplePolicyRecord() ext.DecisionRecord {
	return ext.DecisionRecord{Type: "approval", Origin: "plan", Gate: "schema", Ticket: "CND-12", Project: "CND", Crew: "c1"}
}

func samplePolicy() ext.RoutePolicy {
	return ext.RoutePolicy{
		Revision: "r1", Scope: "s1",
		Pinned:   ext.PinnedRequirement{Rule: "r-1", Groups: []ext.PinnedGroup{{Role: "architecture", Quorum: 1}}, Override: ext.OverrideOperator, Notify: ext.NotifyNow},
		Groups:   []ext.Requirement{{Role: "architecture", Label: "architecture owner", Quorum: 1, Rule: "r-1", Eligible: []ext.Eligible{{Subject: "u-ana", Name: "Ana", Via: ext.ViaRole, Ref: "ref-1"}}}},
		Override: ext.OverrideOperator,
	}
}

// Every bound of a policy, each just inside and just past.
func TestRoutePolicyValid(t *testing.T) {
	ana := func(i int) ext.Eligible {
		return ext.Eligible{Subject: "u-" + strings.Repeat("a", i%10) + string(rune('a'+i%26)) + string(rune('a'+i/26)), Name: "Ana", Via: ext.ViaRole, Ref: "ref"}
	}
	many := func(n int) []ext.Eligible {
		out := make([]ext.Eligible, n)
		for i := range out {
			out[i] = ana(i)
		}
		return out
	}
	groups := func(n int) ([]ext.PinnedGroup, []ext.Requirement) {
		var pg []ext.PinnedGroup
		var rg []ext.Requirement
		for i := range n {
			role := "role-" + string(rune('a'+i))
			pg = append(pg, ext.PinnedGroup{Role: role, Quorum: 1})
			rg = append(rg, ext.Requirement{Role: role, Label: "a role", Quorum: 1, Rule: "r", Eligible: []ext.Eligible{ana(i)}})
		}
		return pg, rg
	}
	cases := []struct {
		name string
		edit func(*ext.RoutePolicy)
		ok   bool
	}{
		{"the sample", func(*ext.RoutePolicy) {}, true},
		{"a revision at its bound", func(p *ext.RoutePolicy) { p.Revision = strings.Repeat("r", ext.MaxPolicyRevision) }, true},
		{"a revision past it", func(p *ext.RoutePolicy) { p.Revision = strings.Repeat("r", ext.MaxPolicyRevision+1) }, false},
		{"no revision", func(p *ext.RoutePolicy) { p.Revision = "" }, false},
		{"a revision with a space", func(p *ext.RoutePolicy) { p.Revision = "r 1" }, false},
		{"a pinned rule at its bound", func(p *ext.RoutePolicy) { p.Pinned.Rule = strings.Repeat("r", ext.MaxPolicyRule) }, true},
		{"a pinned rule past it", func(p *ext.RoutePolicy) { p.Pinned.Rule = strings.Repeat("r", ext.MaxPolicyRule+1) }, false},
		{"four groups", func(p *ext.RoutePolicy) { p.Pinned.Groups, p.Groups = groups(ext.MaxPolicyGroups) }, true},
		{"five groups", func(p *ext.RoutePolicy) { p.Pinned.Groups, p.Groups = groups(ext.MaxPolicyGroups + 1) }, false},
		{"a quorum of four", func(p *ext.RoutePolicy) { p.Pinned.Groups[0].Quorum, p.Groups[0].Quorum = 4, 4 }, true},
		{"a quorum of five", func(p *ext.RoutePolicy) { p.Pinned.Groups[0].Quorum, p.Groups[0].Quorum = 5, 5 }, false},
		{"a quorum of none", func(p *ext.RoutePolicy) { p.Pinned.Groups[0].Quorum, p.Groups[0].Quorum = 0, 0 }, false},
		{"a role at its bound", func(p *ext.RoutePolicy) {
			r := strings.Repeat("a", ext.MaxRoleName)
			p.Pinned.Groups[0].Role, p.Groups[0].Role = r, r
		}, true},
		{"a role past it", func(p *ext.RoutePolicy) {
			r := strings.Repeat("a", ext.MaxRoleName+1)
			p.Pinned.Groups[0].Role, p.Groups[0].Role = r, r
		}, false},
		{"a role in capitals", func(p *ext.RoutePolicy) { p.Pinned.Groups[0].Role, p.Groups[0].Role = "Arch", "Arch" }, false},
		{"a group not the requirement's role", func(p *ext.RoutePolicy) { p.Groups[0].Role = "security" }, false},
		{"a group not the requirement's quorum", func(p *ext.RoutePolicy) { p.Groups[0].Quorum = 2 }, false},
		{"sixteen eligible in a group", func(p *ext.RoutePolicy) { p.Groups[0].Eligible = many(ext.MaxGroupEligible) }, true},
		{"seventeen", func(p *ext.RoutePolicy) { p.Groups[0].Eligible = many(ext.MaxGroupEligible + 1) }, false},
		{"thirty-two eligible across the groups", func(p *ext.RoutePolicy) {
			p.Pinned.Groups, p.Groups = groups(2)
			p.Groups[0].Eligible, p.Groups[1].Eligible = many(16), many(16)
		}, true},
		{"thirty-three", func(p *ext.RoutePolicy) {
			p.Pinned.Groups, p.Groups = groups(3)
			p.Groups[0].Eligible, p.Groups[1].Eligible, p.Groups[2].Eligible = many(16), many(16), many(1)
		}, false},
		{"one person twice in a group", func(p *ext.RoutePolicy) { p.Groups[0].Eligible = append(p.Groups[0].Eligible, p.Groups[0].Eligible[0]) }, false},
		{"a name at its bound", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Name = strings.Repeat("é", ext.MaxEligibleName) }, true},
		{"a name past it", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Name = strings.Repeat("é", ext.MaxEligibleName+1) }, false},
		{"a ref at its bound", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Ref = strings.Repeat("r", ext.MaxEligibleRef) }, true},
		{"a ref past it", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Ref = strings.Repeat("r", ext.MaxEligibleRef+1) }, false},
		{"no ref", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Ref = "" }, false},
		{"a subject at its bound", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Subject = strings.Repeat("s", ext.MaxSubject) }, true},
		{"a subject past it", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Subject = strings.Repeat("s", ext.MaxSubject+1) }, false},
		{"a via of no kind", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Via = "friend" }, false},
		{"a delegate with whom it stands beside", func(p *ext.RoutePolicy) {
			p.Groups[0].Eligible[0].Via, p.Groups[0].Eligible[0].For = ext.ViaDelegate, "u-ben"
		}, true},
		{"a delegate beside no one", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].Via = ext.ViaDelegate }, false},
		{"a for that is no delegate's", func(p *ext.RoutePolicy) { p.Groups[0].Eligible[0].For = "u-ben" }, false},
		{"a label at its bound", func(p *ext.RoutePolicy) { p.Groups[0].Label = strings.Repeat("l", ext.MaxGroupLabel) }, true},
		{"a label past it", func(p *ext.RoutePolicy) { p.Groups[0].Label = strings.Repeat("l", ext.MaxGroupLabel+1) }, false},
		{"a gap with nobody eligible", func(p *ext.RoutePolicy) {
			p.Groups[0].Eligible, p.Groups[0].Gap = nil, strings.Repeat("g", ext.MaxPolicyGap)
		}, true},
		{"a gap past its bound", func(p *ext.RoutePolicy) {
			p.Groups[0].Eligible, p.Groups[0].Gap = nil, strings.Repeat("g", ext.MaxPolicyGap+1)
		}, false},
		{"nobody eligible and no gap", func(p *ext.RoutePolicy) { p.Groups[0].Eligible = nil }, false},
		{"a gap beside an eligible", func(p *ext.RoutePolicy) { p.Groups[0].Gap = "none" }, false},
		{"a gap of two lines", func(p *ext.RoutePolicy) { p.Groups[0].Eligible, p.Groups[0].Gap = nil, "a\nb" }, false},
		{"an override of none", func(p *ext.RoutePolicy) { p.Override, p.Pinned.Override = ext.OverrideNone, ext.OverrideNone }, true},
		{"an override of another word", func(p *ext.RoutePolicy) { p.Override, p.Pinned.Override = "anyone", "anyone" }, false},
		{"an override not the pinned one", func(p *ext.RoutePolicy) { p.Override = ext.OverrideNone }, false},
		{"distinct not the pinned one", func(p *ext.RoutePolicy) { p.Distinct = true }, false},
		{"notify batch", func(p *ext.RoutePolicy) { p.Pinned.Notify = ext.NotifyBatch }, true},
		{"notify of another word", func(p *ext.RoutePolicy) { p.Pinned.Notify = "later" }, false},
		{"escalateAfter at its least", func(p *ext.RoutePolicy) { p.Pinned.EscalateAfter = ext.MinEscalateAfter }, true},
		{"escalateAfter under it", func(p *ext.RoutePolicy) { p.Pinned.EscalateAfter = ext.MinEscalateAfter - time.Second }, false},
		{"escalateAfter at its most", func(p *ext.RoutePolicy) { p.Pinned.EscalateAfter = ext.MaxEscalateAfter }, true},
		{"escalateAfter past it", func(p *ext.RoutePolicy) { p.Pinned.EscalateAfter = ext.MaxEscalateAfter + time.Second }, false},
		{"the unsupported form", func(p *ext.RoutePolicy) {
			p.Pinned.Groups, _ = groups(2)
			p.Unsupported = true
			p.Groups = []ext.Requirement{{Role: "role-a", Label: "a role", Quorum: 1, Rule: "r", Gap: "this requirement cannot be routed"}}
		}, true},
		{"the unsupported form with someone eligible", func(p *ext.RoutePolicy) { p.Unsupported = true }, false},
		{"sixteen recent changes", func(p *ext.RoutePolicy) {
			for range ext.MaxPolicyRecent {
				p.Recent = append(p.Recent, ext.RouteChange{Why: ext.Rerouted, Revision: "r2", At: time.Now().UTC()})
			}
		}, true},
		{"seventeen", func(p *ext.RoutePolicy) {
			for range ext.MaxPolicyRecent + 1 {
				p.Recent = append(p.Recent, ext.RouteChange{Why: ext.Rerouted, Revision: "r2", At: time.Now().UTC()})
			}
		}, false},
		{"a change of no kind", func(p *ext.RoutePolicy) { p.Recent = []ext.RouteChange{{Why: "moved", Revision: "r2"}} }, false},
		{"reroutes below none", func(p *ext.RoutePolicy) { p.Reroutes = -1 }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := samplePolicy()
			c.edit(&p)
			if err := p.Valid(samplePolicyRecord()); (err == nil) != c.ok {
				t.Fatalf("Valid = %v, want ok %v", err, c.ok)
			}
		})
	}
}

// The scope: 1 to MaxPolicyScope bytes of printable ASCII.
func TestRoutePolicyScopeValid(t *testing.T) {
	for _, c := range []struct {
		scope string
		ok    bool
	}{
		{"s", true}, {strings.Repeat("s", ext.MaxPolicyScope), true}, {strings.Repeat("s", ext.MaxPolicyScope+1), false},
		{"", false}, {"a scope", false}, {"é", false},
	} {
		p := samplePolicy()
		p.Scope = c.scope
		if err := p.Valid(samplePolicyRecord()); (err == nil) != c.ok {
			t.Errorf("scope %q: %v, want ok %v", c.scope, err, c.ok)
		}
	}
}

// A policy is a durable decision's record's: on a screen's question (no
// Request, no Record) a route carrying one is invalid, and on a record
// it is valid when the policy is.
func TestPolicyOnlyWithARecord(t *testing.T) {
	p := samplePolicy()
	screen := ext.Decision{ID: "q1", Message: "Allow?", Kind: "permission"}
	if err := (ext.Route{Policy: &p}).Valid(screen); err == nil {
		t.Fatal("a policy on a screen's question is valid")
	}
	noRecord := ext.Decision{ID: "dr-7", Request: "dr-7", Kind: "decision"}
	if err := (ext.Route{Policy: &p}).Valid(noRecord); err == nil {
		t.Fatal("a policy on a record that says nothing of itself is valid")
	}
	rec := samplePolicyRecord()
	withRecord := noRecord
	withRecord.Record = &rec
	if err := (ext.Route{Policy: &p}).Valid(withRecord); err != nil {
		t.Fatalf("a policy on a record: %v", err)
	}
	bad := samplePolicy()
	bad.Revision = ""
	if err := (ext.Route{Policy: &bad}).Valid(withRecord); err == nil {
		t.Fatal("an invalid policy on a record is valid")
	}
}

// A policy with every field at its bound in plain text (four groups of
// quorum four, thirty-two eligible, sixteen recent changes) is valid and
// within MaxPolicy as encoded; the same policy whose names escape as JSON
// escapes them (six bytes a character) passes MaxPolicy and is refused:
// the bound is of the policy as encoded, escapes and all.
func TestRoutePolicyBound(t *testing.T) {
	fullest := func(name string) ext.RoutePolicy {
		p := ext.RoutePolicy{
			Revision: strings.Repeat("r", ext.MaxPolicyRevision), Scope: strings.Repeat("s", ext.MaxPolicyScope),
			Pinned:   ext.PinnedRequirement{Rule: strings.Repeat("r", ext.MaxPolicyRule), Distinct: true, Override: ext.OverrideOperator, Notify: ext.NotifyBatch, EscalateAfter: ext.MaxEscalateAfter},
			Distinct: true, Override: ext.OverrideOperator, Reroutes: 1 << 30,
		}
		n := 0
		for g := range ext.MaxPolicyGroups {
			role := strings.Repeat(string(rune('a'+g)), ext.MaxRoleName)
			p.Pinned.Groups = append(p.Pinned.Groups, ext.PinnedGroup{Role: role, Quorum: ext.MaxGroupQuorum})
			req := ext.Requirement{Role: role, Label: strings.Repeat("l", ext.MaxGroupLabel), Quorum: ext.MaxGroupQuorum, Rule: strings.Repeat("r", ext.MaxPolicyRule)}
			for range ext.MaxPolicyEligible / ext.MaxPolicyGroups {
				n++
				subject := strings.Repeat("s", ext.MaxSubject-2) + string(rune('a'+n/26)) + string(rune('a'+n%26))
				req.Eligible = append(req.Eligible, ext.Eligible{Subject: subject, Name: strings.Repeat(name, ext.MaxEligibleName), Via: ext.ViaDelegate, For: strings.Repeat("f", ext.MaxSubject), Ref: strings.Repeat("r", ext.MaxEligibleRef)})
			}
			p.Groups = append(p.Groups, req)
		}
		for range ext.MaxPolicyRecent {
			p.Recent = append(p.Recent, ext.RouteChange{Why: ext.RerouteDelegated, Revision: strings.Repeat("r", 12), At: time.Date(2026, 11, 19, 23, 59, 59, 999, time.UTC)})
		}
		return p
	}
	plain := fullest("n")
	b, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > ext.MaxPolicy {
		t.Fatalf("the fullest plain policy is %d bytes, past %d", len(b), ext.MaxPolicy)
	}
	if err := plain.Valid(samplePolicyRecord()); err != nil {
		t.Fatalf("the fullest plain policy (%d bytes): %v", len(b), err)
	}
	escaped := fullest("<")
	b, _ = json.Marshal(escaped)
	if len(b) <= ext.MaxPolicy {
		t.Fatalf("the escaping policy is %d bytes, within %d: it tests nothing", len(b), ext.MaxPolicy)
	}
	if err := escaped.Valid(samplePolicyRecord()); err == nil || !strings.Contains(err.Error(), "as encoded") {
		t.Fatalf("a policy of %d bytes as encoded: %v", len(b), err)
	}
}

// A confirmation: Behind and Groups only in force, at most four roles, a
// revision of printable ASCII, a why of one line within its bound.
func TestConfirmationValid(t *testing.T) {
	for _, c := range []struct {
		name string
		c    ext.Confirmation
		ok   bool
	}{
		{"not in force", ext.Confirmation{Why: "no policy"}, true},
		{"in force with groups", ext.Confirmation{InForce: true, Groups: []string{"architecture", "security", "product", "acceptance"}}, true},
		{"five groups", ext.Confirmation{InForce: true, Groups: []string{"a", "b", "c", "d", "e"}}, false},
		{"a group twice", ext.Confirmation{InForce: true, Groups: []string{"a", "a"}}, false},
		{"a group of no role's form", ext.Confirmation{InForce: true, Groups: []string{"Arch"}}, false},
		{"groups not in force", ext.Confirmation{Groups: []string{"a"}}, false},
		{"behind in force", ext.Confirmation{InForce: true, Behind: true}, true},
		{"behind not in force", ext.Confirmation{Behind: true}, false},
		{"routes", ext.Confirmation{Routes: true}, true},
		{"a revision", ext.Confirmation{InForce: true, Revision: "r2"}, true},
		{"a revision with a space", ext.Confirmation{InForce: true, Revision: "r 2"}, false},
		{"a why at its bound", ext.Confirmation{Why: strings.Repeat("w", ext.MaxConfirmWhy)}, true},
		{"a why past it", ext.Confirmation{Why: strings.Repeat("w", ext.MaxConfirmWhy+1)}, false},
		{"a why of two lines", ext.Confirmation{Why: "a\nb"}, false},
	} {
		if err := c.c.Valid(); (err == nil) != c.ok {
			t.Errorf("%s: %v, want ok %v", c.name, err, c.ok)
		}
	}
}

// A re-route: a decision's id, a policy and no answer, one of the four
// whys, an operation's id and a rev from 1.
func TestRerouteRequestValid(t *testing.T) {
	p := samplePolicy()
	good := ext.RerouteRequest{Decision: "dr-7", Route: ext.Route{Policy: &p}, Why: ext.Rerouted, OpID: "op-1", ExpectedRev: 2}
	if err := good.Valid(); err != nil {
		t.Fatalf("a good re-route: %v", err)
	}
	for _, c := range []struct {
		name string
		edit func(*ext.RerouteRequest)
	}{
		{"not a decision's id", func(r *ext.RerouteRequest) { r.Decision = "q1" }},
		{"a leading zero", func(r *ext.RerouteRequest) { r.Decision = "dr-07" }},
		{"no policy", func(r *ext.RerouteRequest) { r.Route.Policy = nil }},
		{"an answer", func(r *ext.RerouteRequest) { r.Route.Answer = &ext.Answer{Input: "1"} }},
		{"a why of no kind", func(r *ext.RerouteRequest) { r.Why = "moved" }},
		{"no op id", func(r *ext.RerouteRequest) { r.OpID = "" }},
		{"an op id past its bound", func(r *ext.RerouteRequest) { r.OpID = strings.Repeat("o", ext.MaxOpID+1) }},
		{"no rev", func(r *ext.RerouteRequest) { r.ExpectedRev = 0 }},
	} {
		r := good
		c.edit(&r)
		if r.Valid() == nil {
			t.Errorf("%s: valid", c.name)
		}
	}
}

// A notice's recipient: a subject's shape, or none.
func TestNotificationRecipientValid(t *testing.T) {
	for _, c := range []struct {
		r  string
		ok bool
	}{{"", true}, {"u-ana", true}, {strings.Repeat("s", ext.MaxSubject), true}, {strings.Repeat("s", ext.MaxSubject+1), false}, {"u ana", false}, {"ü", false}} {
		n := ext.Notification{Message: "m", Recipient: c.r}
		if err := n.Valid(); (err == nil) != c.ok {
			t.Errorf("recipient %q: %v, want ok %v", c.r, err, c.ok)
		}
	}
}

// A notice's key: at most MaxNoticeKey bytes of printable ASCII, or none.
func TestNotificationKeyValid(t *testing.T) {
	for _, c := range []struct {
		k  string
		ok bool
	}{{"", true}, {"k-1", true}, {strings.Repeat("k", ext.MaxNoticeKey), true}, {strings.Repeat("k", ext.MaxNoticeKey+1), false}, {"k 1", false}} {
		n := ext.Notification{Message: "m", Key: c.k}
		if err := n.Valid(); (err == nil) != c.ok {
			t.Errorf("key %q: %v, want ok %v", c.k, err, c.ok)
		}
	}
}

// Every final state has its outcome; By and Via are empty together.
func TestResolutionOutcomeValid(t *testing.T) {
	for _, state := range []string{"applied", "superseded", "cancelled", "expired"} {
		if err := (ext.DecisionOutcome{State: state}).Valid(); err != nil {
			t.Errorf("%s with no one deciding it: %v", state, err)
		}
		if err := (ext.DecisionOutcome{State: state, By: "u-ana", Via: ext.OutcomeEligible}).Valid(); err != nil {
			t.Errorf("%s decided by an eligible: %v", state, err)
		}
	}
	for _, c := range []struct {
		name string
		o    ext.DecisionOutcome
	}{
		{"an open state", ext.DecisionOutcome{State: "open"}},
		{"answered is not final", ext.DecisionOutcome{State: "answered"}},
		{"by without via", ext.DecisionOutcome{State: "applied", By: "operator"}},
		{"via without by", ext.DecisionOutcome{State: "applied", Via: ext.OutcomeLocal}},
		{"a via of no kind", ext.DecisionOutcome{State: "applied", By: "operator", Via: "luck"}},
		{"superseded by a decision of another state", ext.DecisionOutcome{State: "applied", SupersededBy: "dr-9"}},
		{"superseded by no decision's id", ext.DecisionOutcome{State: "superseded", SupersededBy: "q9"}},
		{"a why of two lines", ext.DecisionOutcome{State: "cancelled", Why: "a\nb"}},
	} {
		if c.o.Valid() == nil {
			t.Errorf("%s: valid", c.name)
		}
	}
	if err := (ext.DecisionOutcome{State: "superseded", By: "operator", Via: ext.OutcomeOverride, SupersededBy: "dr-9", Why: "moved"}).Valid(); err != nil {
		t.Errorf("a superseded override: %v", err)
	}
}
