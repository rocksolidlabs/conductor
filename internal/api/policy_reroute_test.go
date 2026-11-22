package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A router's policy after the opening (SW1 PR-7b,
// docs/plans/decision-policy.md): the router's re-route and its reads
// back (ext.Host's OpenDecisions, Outcomes, AckOutcomes, Reroute), who may
// change a route under a policy, and who sees a routed decision on the
// decide routes. The router is exttest's FakeRouter; its Host is the one
// the server gives a provider of its name.

// routerHost is the Host the server gives the router named switchman.
func (f *policyFixture) routerHost(t *testing.T) *extHost {
	return &extHost{s: f.srv, name: "switchman", ctx: t.Context()}
}

// reroute re-routes did to the policy p as the router, from rev.
func (f *policyFixture) reroute(t *testing.T, did string, p ext.RoutePolicy, why, opID string, rev int) (ext.RerouteResult, error) {
	t.Helper()
	return f.routerHost(t).Reroute(t.Context(), ext.RerouteRequest{Decision: did, Route: ext.Route{Policy: &p}, Why: why, OpID: opID, ExpectedRev: rev})
}

// withEligible is p at revision rev with subject the one eligible.
func withEligible(p ext.RoutePolicy, rev, subject string) ext.RoutePolicy {
	p = *p.Clone()
	p.Revision = rev
	p.Groups[0].Eligible = []ext.Eligible{{Subject: subject, Name: strings.ToUpper(subject[:1]) + subject[1:], Via: ext.ViaAssigned, Ref: "ref-" + subject}}
	p.Groups[0].Gap = ""
	return p
}

// routerRows are did's history rows of a router's changes.
func routerRows(rec ledger.DecisionRecord) []ledger.HistoryRow {
	var out []ledger.HistoryRow
	for _, h := range rec.History {
		switch h.Kind {
		case ledger.HistoryAssigned, ledger.HistoryDelegated, ledger.HistoryEscalated, ledger.HistoryRerouted:
			out = append(out, h)
		}
	}
	return out
}

// A re-route writes its policy, rev + 1, and one history row of its kind
// with its op id; who may answer follows it: bob, now named, answers.
func TestRerouteWritesItsHistoryKind(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	rev := f.rev(t, did)
	res, err := f.reroute(t, did, withEligible(anaPolicy(ext.OverrideNone), "r2", "bob"), ext.RerouteDelegated, "op-1", rev)
	if err != nil || res.Rev != rev+1 || res.Unchanged {
		t.Fatalf("the re-route: %+v %v", res, err)
	}
	rec := f.record(t, did)
	rows := routerRows(rec)
	if len(rows) != 1 || rows[0].Kind != ledger.HistoryDelegated || rows[0].OpID != "op-1" || rows[0].By != "router:switchman" {
		t.Fatalf("the router's rows: %+v", rows)
	}
	p, _, _ := crew.RouterPolicyOf(rec.Policy)
	if p.Revision != "r2" || p.Groups[0].Eligible[0].Subject != "bob" || p.Reroutes != 1 || !ledger.PolicySpare(rec.Policy) {
		t.Fatalf("the policy after the re-route: %s", rec.Policy)
	}
	f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
		if q.Subject == "bob" {
			return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
		}
		return ext.Confirmation{InForce: true}
	})
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, rec.Rev); status != http.StatusNotFound {
		t.Fatalf("ana, no longer named: %d %s", status, data)
	}
	if status, data, _ := f.respondAs(t, passBob, did, "b1", 0, rec.Rev); status != http.StatusCreated {
		t.Fatalf("bob, now named: %d %s", status, data)
	}
}

// Forty re-routes of one decision: no new decision, sixteen history rows,
// Reroutes forty, Recent the latest sixteen past the history's; each
// re-route answered its rev.
func TestManyReroutesKeepTheDecision(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	for i := 1; i <= 40; i++ {
		rev := f.rev(t, did)
		if _, err := f.reroute(t, did, withEligible(anaPolicy(ext.OverrideNone), fmt.Sprintf("r%d", i+1), "ana"), ext.Rerouted, fmt.Sprintf("op-%d", i), rev); err != nil {
			t.Fatalf("re-route %d: %v", i, err)
		}
	}
	rec := f.record(t, did)
	p, _, _ := crew.RouterPolicyOf(rec.Policy)
	if n := len(routerRows(rec)); n != ledger.MaxDecisionRouterChanges {
		t.Fatalf("%d router rows", n)
	}
	if p.Reroutes != 40 || len(p.Recent) != ext.MaxPolicyRecent || p.Recent[0].Revision != "r41" || p.Recent[15].Revision != "r26" {
		t.Fatalf("reroutes %d, recent %+v", p.Reroutes, p.Recent)
	}
	if _, ok, _ := f.srv.ledger.Decision(t.Context(), 2); ok {
		t.Fatal("a re-route opened a decision")
	}
	if rec.State != ledger.DecisionOpen {
		t.Fatalf("the decision is %s", rec.State)
	}
}

// Twenty re-routes; the twentieth's reply lost, it is sent again: its
// revision is the record's, so it answers Unchanged and writes nothing,
// past the history's room as before it.
func TestLostRerouteReplyPastTheHistory(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	var last ext.RoutePolicy
	var lastRev int
	for i := 1; i <= 20; i++ {
		last = withEligible(anaPolicy(ext.OverrideNone), fmt.Sprintf("r%d", i+1), "ana")
		lastRev = f.rev(t, did)
		if _, err := f.reroute(t, did, last, ext.Rerouted, fmt.Sprintf("op-%d", i), lastRev); err != nil {
			t.Fatalf("re-route %d: %v", i, err)
		}
	}
	before := f.record(t, did)
	res, err := f.reroute(t, did, last, ext.Rerouted, "op-20", lastRev)
	if err != nil || !res.Unchanged || res.Rev != before.Rev {
		t.Fatalf("the twentieth sent again: %+v %v", res, err)
	}
	if after := f.record(t, did); after.Rev != before.Rev || after.Policy != before.Policy || len(after.History) != len(before.History) {
		t.Fatal("a repeat wrote")
	}
}

// An older computation completing after a newer one: ErrStale, naming the
// rev and revision now, nothing written.
func TestLateRerouteCannotRestoreAnOlderPolicy(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	rev := f.rev(t, did)
	if _, err := f.reroute(t, did, withEligible(anaPolicy(ext.OverrideNone), "r3", "bob"), ext.Rerouted, "op-new", rev); err != nil {
		t.Fatal(err)
	}
	before := f.record(t, did)
	_, err := f.reroute(t, did, withEligible(anaPolicy(ext.OverrideNone), "r2", "ana"), ext.Rerouted, "op-old", rev)
	if !errors.Is(err, ext.ErrStale) || !strings.Contains(err.Error(), "r3") {
		t.Fatalf("the late re-route: %v", err)
	}
	if after := f.record(t, did); after.Policy != before.Policy || after.Rev != before.Rev {
		t.Fatal("the late re-route wrote")
	}
}

// A re-route never changes the requirement: another pinned requirement or
// scope is refused, nothing written.
func TestRerouteCannotChangeTheRequirement(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	rev := f.rev(t, did)
	for name, edit := range map[string]func(*ext.RoutePolicy){
		"its override": func(p *ext.RoutePolicy) { p.Override, p.Pinned.Override = ext.OverrideOperator, ext.OverrideOperator },
		"its rule":     func(p *ext.RoutePolicy) { p.Pinned.Rule = "r-9" },
		"its notify":   func(p *ext.RoutePolicy) { p.Pinned.Notify = ext.NotifyBatch },
		"its scope":    func(p *ext.RoutePolicy) { p.Scope = "elsewhere" },
	} {
		p := withEligible(anaPolicy(ext.OverrideNone), "r2", "bob")
		edit(&p)
		if _, err := f.reroute(t, did, p, ext.Rerouted, "op-"+strings.ReplaceAll(name, " ", "-"), rev); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("a re-route changing %s: %v", name, err)
		}
	}
	if got := f.record(t, did); got.Rev != rev {
		t.Fatalf("rev %d after the refusals", got.Rev)
	}
}

// The requirement is written with the first policy and read back from the
// record unchanged after a re-route.
func TestRequirementRequiredAndUnchangedByAReroute(t *testing.T) {
	f := newPolicyFixture(t)
	first := anaPolicy(ext.OverrideNone)
	first.Pinned.Notify, first.Pinned.EscalateAfter = ext.NotifyBatch, ext.MinEscalateAfter
	did := f.routed(t, "r1", "dr-1", first)
	next := withEligible(first, "r2", "bob")
	if _, err := f.reroute(t, did, next, ext.Rerouted, "op-1", f.rev(t, did)); err != nil {
		t.Fatal(err)
	}
	p, _, _ := crew.RouterPolicyOf(f.record(t, did).Policy)
	if p.Pinned.Notify != ext.NotifyBatch || p.Pinned.EscalateAfter != ext.MinEscalateAfter || p.Pinned.Rule != first.Pinned.Rule {
		t.Fatalf("the requirement after the re-route: %+v", p.Pinned)
	}
	empty := next
	empty.Pinned = ext.PinnedRequirement{}
	if _, err := f.reroute(t, did, empty, ext.Rerouted, "op-2", f.rev(t, did)); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a policy with no requirement: %v", err)
	}
}

// The one change of form is from the unsupported form to the
// requirement's own groups: back is refused.
func TestUnsupportedFormToFullIsTheOneChange(t *testing.T) {
	f := newPolicyFixture(t)
	unsupported := anaPolicy(ext.OverrideNone)
	unsupported.Unsupported = true
	unsupported.Groups[0].Eligible, unsupported.Groups[0].Gap = nil, "this requirement cannot be routed yet"
	did := f.routed(t, "r1", "dr-1", unsupported)
	full := withEligible(anaPolicy(ext.OverrideNone), "r2", "ana")
	if _, err := f.reroute(t, did, full, ext.Rerouted, "op-1", f.rev(t, did)); err != nil {
		t.Fatalf("to the requirement's groups: %v", err)
	}
	back := unsupported
	back.Revision = "r3"
	if _, err := f.reroute(t, did, back, ext.Rerouted, "op-2", f.rev(t, did)); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("back to the unsupported form: %v", err)
	}
}

// Only the registered router's Host re-routes and reads the policies back:
// another provider's Host answers ErrUnsupported for each.
func TestRerouteOnlyForTheRouter(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	other := &extHost{s: f.srv, name: "cloud", ctx: t.Context()}
	p := withEligible(anaPolicy(ext.OverrideNone), "r2", "bob")
	if _, err := other.Reroute(t.Context(), ext.RerouteRequest{Decision: did, Route: ext.Route{Policy: &p}, Why: ext.Rerouted, OpID: "op-1", ExpectedRev: f.rev(t, did)}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("another provider's re-route: %v", err)
	}
	if _, err := other.OpenDecisions(t.Context(), ""); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("another provider's open decisions: %v", err)
	}
	if _, err := other.Outcomes(t.Context(), 0); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("another provider's outcomes: %v", err)
	}
	if err := other.AckOutcomes(t.Context(), 1<<40); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("another provider's acknowledgement: %v", err)
	}
	desk := &extHost{s: f.srv, name: "switchman", ctx: t.Context(), desk: true}
	if _, err := desk.OpenDecisions(t.Context(), ""); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("the desk's open decisions: %v", err)
	}
	if got := f.record(t, did); routerRows(got) != nil {
		t.Fatalf("a refused re-route wrote: %+v", routerRows(got))
	}
}

// A comparison's selection takes no router's policy from a re-route.
func TestRerouteRefusesASelection(t *testing.T) {
	f := newPolicyFixture(t)
	var n int64
	if _, err := f.srv.ledger.Transition(t.Context(), f.run, func(tx *ledger.Tx) error {
		got, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginCompare, Type: ledger.DecisionApproval, Fingerprint: "fp-cmp",
			Request: `{"question":"Which approach?","options":[{"label":"approach-a at 4b825dc","subject":{"kind":"candidate","run":"` + f.run + `","task":"approach-a","n":1,"commit":"4b825dc"}},` +
				`{"label":"None of these"}],"recommendation":-1,"authorizes":"Continuing the selected candidate.","subject":{"kind":"comparison","run":"` + f.run + `","base":"abc123","digest":"d1"}}`,
			Policy: `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`})
		n = got.N
		return err
	}); err != nil {
		t.Fatal(err)
	}
	did := ledger.DecisionID(n)
	if _, err := f.reroute(t, did, anaPolicy(ext.OverrideNone), ext.Rerouted, "op-1", 1); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a re-route of a selection: %v", err)
	}
	if rec := f.record(t, did); ledger.RouterPolicy(rec.Policy) {
		t.Fatalf("the selection's policy: %s", rec.Policy)
	}
}

// OpenDecisions pages every open record, 64 a page in number order, each
// with its policy (nil under the server's own), no count cut; the router's
// Host alone reads it.
func TestOpenDecisionsPagesEveryRecord(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	ids := f.seed(t, 140, seedRequest(""))
	var got []string
	var routed *ext.OpenDecision
	after := ""
	for pages := 0; ; pages++ {
		page, err := f.routerHost(t).OpenDecisions(t.Context(), after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Decisions) > 64 || pages > 10 {
			t.Fatalf("a page of %d", len(page.Decisions))
		}
		for i := range page.Decisions {
			d := page.Decisions[i]
			got = append(got, d.Decision.Request)
			if d.Decision.Request == did {
				routed = &d
			} else if d.Policy != nil {
				t.Fatalf("%s carries a policy", d.Decision.Request)
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	want := append([]string{did}, ids...)
	slices.SortFunc(want, func(a, b string) int {
		na, _ := ledger.ParseDecisionID(a)
		nb, _ := ledger.ParseDecisionID(b)
		return int(na - nb)
	})
	if !slices.Equal(got, want) {
		t.Fatalf("paged %d records, want %d", len(got), len(want))
	}
	if routed == nil || routed.Policy == nil || routed.Policy.Revision != "exttest-r1" || routed.RoutedAt.IsZero() || routed.Decision.Record == nil {
		t.Fatalf("the routed record's page entry: %+v", routed)
	}
}

// The outbox is read in seq order by the router alone and leaves only by
// its acknowledgement: a routed record cancelled is read with its outcome,
// read again until acknowledged, and gone after.
func TestOutcomeOutboxAckedOnlyByTheRouter(t *testing.T) {
	f := newPolicyFixture(t)
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/cancel", map[string]any{"commandId": "c1", "why": "not needed now", "expectedRev": f.rev(t, did)}, http.StatusOK, "")
	f.decisionWhen(t, did, "cancelled", inState(ledger.DecisionCancelled))
	h := f.routerHost(t)
	for range 2 {
		page, err := h.Outcomes(t.Context(), 0)
		if err != nil || len(page.Outcomes) != 1 {
			t.Fatalf("the outbox: %+v %v", page, err)
		}
		o := page.Outcomes[0]
		if o.Decision.Request != did || o.Outcome.State != ledger.DecisionCancelled || o.Outcome.By != "" || o.Policy == nil || o.Outcome.Valid() != nil {
			t.Fatalf("the outcome: %+v", o)
		}
	}
	if err := (&extHost{s: f.srv, name: "cloud", ctx: t.Context()}).AckOutcomes(t.Context(), 1<<40); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("another's acknowledgement: %v", err)
	}
	if page, _ := h.Outcomes(t.Context(), 0); len(page.Outcomes) != 1 {
		t.Fatal("another's acknowledgement took the row")
	}
	page, _ := h.Outcomes(t.Context(), 0)
	if err := h.AckOutcomes(t.Context(), page.Outcomes[0].Seq); err != nil {
		t.Fatal(err)
	}
	if page, _ := h.Outcomes(t.Context(), 0); len(page.Outcomes) != 0 {
		t.Fatalf("after the acknowledgement: %+v", page)
	}
	// Its end told to the router carries its outcome.
	until(t, did+" resolved", func() bool {
		for _, r := range f.fr.Resolutions() {
			if r.Request == did {
				return r.Outcome != nil && r.Outcome.State == ledger.DecisionCancelled
			}
		}
		return false
	})
}

// Under a policy in force the operator's route to a person, a role or the
// decide grant is refused routed_by_policy, nothing written; not in force,
// A route to the operator's own person while the policy is not in force
// changes whom the record is for, and the router is told the record again
// A route to the router asks it again: a routed row naming it, no rev,
// The router no longer names ana (its re-route held back, its confirmation
// already says so): the decide list, the read and the respond route all
// A decide principal sees only the routed decisions it is eligible for
// A routed row the router does not confirm in time (its Confirm held past
// The decision inbox names a routed record's persons and its rule from
// Every record is told at its opening, whatever the sessions' questions'
// bound holds (maxUnresolved): the bound is the sessions', not the
// records'.
func TestRecordsToldPastMaxUnresolved(t *testing.T) {
	f := newPolicyFixture(t)
	f.srv.router.mu.Lock()
	f.srv.router.bound = 0
	f.srv.router.mu.Unlock()
	did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideNone))
	if !ledger.RouterPolicy(f.record(t, did).Policy) {
		t.Fatal("a record past the bound was not routed")
	}
}

// A decision link's lane answers no record, routed or not: its answer is

// A page whose records all ended while it was read (between the open
// decisions' index and their records' reads, held at the ledger's seam)
// comes back with no record and still names where the next page starts:
// the walk by Next reaches the records of the later pages, and ends.
func TestOpenDecisionsNamesTheNextPageWhenAPagesRecordsEnded(t *testing.T) {
	f := newPolicyFixture(t)
	ids := f.seed(t, 70, seedRequest(""))
	var once sync.Once
	f.srv.ledger.OnOpenRecordsRead(func(ns []int64) {
		once.Do(func() {
			// Every number of the first page ends before its record is read.
			f.rawWrite(t, `UPDATE decisions SET state = 'cancelled', why = 'ended while listed' WHERE n <= ?`, ns[len(ns)-1])
		})
	})
	t.Cleanup(func() { f.srv.ledger.OnOpenRecordsRead(nil) })
	first, err := f.routerHost(t).OpenDecisions(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Decisions) != 0 || first.Next != ids[63] {
		t.Fatalf("the first page: %d records, next %q, want none and %s", len(first.Decisions), first.Next, ids[63])
	}
	var got []string
	after := first.Next
	for pages := 0; after != ""; pages++ {
		page, err := f.routerHost(t).OpenDecisions(t.Context(), after)
		if err != nil || pages > 3 {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, d := range page.Decisions {
			got = append(got, d.Decision.Request)
		}
		after = page.Next
	}
	if strings.Join(got, ",") != strings.Join(ids[64:], ",") {
		t.Fatalf("the walk read %v, want %v", got, ids[64:])
	}
}
