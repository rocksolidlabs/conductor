package crew

import (
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// routedPolicyJSON is a router's policy naming ana in its one group.
const routedPolicyJSON = `{"revision":"r1","approver":"router","quorum":1,"authority":[{"kind":"operator"}],"router":"switchman","scope":"s1",` +
	`"pinned":{"rule":"r-1","groups":[{"role":"architecture","quorum":1}],"override":"operator","notify":"now"},` +
	`"groups":[{"role":"architecture","label":"architecture owner","quorum":1,"rule":"r-1","eligible":[{"subject":"ana","name":"Ana","via":"role","ref":"ref-1"}]}],` +
	`"override":"operator","routedAt":"2026-11-19T09:00:00Z","spare":true,"fallback":"local/1"}`

// Under a router's policy the entries are the operator's and one eligible
// entry per person it names, never the decide grant's, whatever the
// request's for says: ana, named, holding the decide grant (or the admin
// grant) through an identity, is admitted to the open record; bob,
// holding the decide grant, is not; ana through no identity, or holding
// only the host grant, is not. One judgement, AdmissionOf, for the list
// and the read alike.
func TestRouterEntryIsTheOneCheck(t *testing.T) {
	for _, given := range []bool{true, false} {
		rec := ledger.DecisionRecord{DecisionRow: ledger.DecisionRow{N: 7, State: ledger.DecisionOpen, Policy: routedPolicyJSON, Request: grantedRequest(given)}}
		entries := EntriesOf(rec)
		if len(entries) != 2 || entries[0].Kind != EntryOperator || entries[1] != (PolicyEntry{Kind: EntryEligible, Subject: "ana"}) {
			t.Fatalf("given %v: the entries %+v", given, entries)
		}
		for _, c := range []struct {
			name string
			who  Admittee
			want Admission
		}{
			{"ana deciding", Admittee{Grants: []string{"decide"}, Via: ViaIdentity, Subject: "ana"}, AdmitOpen},
			{"bob deciding", Admittee{Grants: []string{"decide"}, Via: ViaIdentity, Subject: "bob"}, AdmitNone},
			{"ana with the host grant alone", Admittee{Grants: []string{"host"}, Via: ViaIdentity, Subject: "ana"}, AdmitNone},
			{"ana through a router", Admittee{Grants: []string{"decide"}, Via: ViaRouter, Subject: "ana"}, AdmitNone},
			{"the operator", Admittee{Grants: []string{"admin"}, Via: ViaWorkbench}, AdmitWhole},
		} {
			if got := AdmissionOf(rec, c.who); got != c.want {
				t.Errorf("given %v, %s: %d, want %d", given, c.name, got, c.want)
			}
		}
		if !Admits(entries, Admittee{Grants: []string{"admin"}, Via: ViaIdentity, Subject: "ana"}) {
			t.Errorf("given %v: ana holding the admin grant is not admitted by her entry", given)
		}
	}
}
