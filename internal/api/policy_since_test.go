package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A router's word on a query with a Since (ext.SinceConfirmer,
// docs/plans/decision-policy.md), at the routes: the server asks such a
// query only of a router that says it answers one. Each router is
// exttest's FakeRouter in one of its forms: NewFakeRouter's answers a
// Since, NewFakeRouterNoSince's says it answers none (and answers one not
// eligible), WithoutSince's does not say. Every test waits on the record in
// the ledger.

// sinceForms are the fake router's three forms, each with the fake that
// records its queries and whether it answers a Since.
func sinceForms() []struct {
	name   string
	fr     *exttest.FakeRouter
	router ext.DecisionRouter
	since  bool
} {
	yes := exttest.NewFakeRouter("switchman")
	no := exttest.NewFakeRouterNoSince("switchman")
	plain := exttest.NewFakeRouter("switchman")
	return []struct {
		name   string
		fr     *exttest.FakeRouter
		router ext.DecisionRouter
		since  bool
	}{
		{"answers a since", yes, yes, true},
		{"says it answers none", no, no, false},
		{"does not say", plain, exttest.WithoutSince(plain), false},
	}
}

// sinceQueries counts fr's queries about subject for did, and those of
// them with a Since.
func sinceQueries(fr *exttest.FakeRouter, did, subject string) (all, since int) {
	for _, q := range fr.Confirms() {
		if q.Decision.Request == did && q.Subject == subject {
			all++
			if !q.Since.IsZero() {
				since++
			}
		}
	}
	return all, since
}

// A policy of one place, answered by ana, eligible, through the decide
// route: applied once and left applied, no fresh decision opened, with
// every form of the router. Only the router that answers a Since is asked
// one, at the application; the others are asked the plain query there.
func TestAOnePlaceAnswerIsAppliedWhateverTheRouterSaysOfASince(t *testing.T) {
	for _, c := range sinceForms() {
		t.Run(c.name, func(t *testing.T) {
			f := &policyFixture{newDecideFixtureRouted(t, nil, c.router), c.fr}
			did := f.routed(t, "r1", "dr-1", anaPolicy(ext.OverrideOperator))
			f.inForce(did)
			if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did)); status != http.StatusCreated {
				t.Fatalf("ana's answer: %d %s", status, data)
			}
			f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
			all, since := sinceQueries(c.fr, did, "ana")
			want := 0
			if c.since {
				want = 1
			}
			if all != 3 || since != want {
				t.Fatalf("the router was asked about ana %d times, %d with a Since; want 3, %d with a Since", all, since, want)
			}
			if rec := f.record(t, did); rec.State != ledger.DecisionApplied {
				t.Fatalf("after its application %s is %s", did, rec.State)
			}
			n, _ := ledger.ParseDecisionID("dr-2")
			if _, found, err := f.srv.ledger.Decision(t.Context(), n); err != nil || found {
				t.Fatalf("a fresh decision after the application: %v %v", found, err)
			}
		})
	}
}

// A policy of two groups from a router that answers no Since, either form:
// not taken. The record stays under the server's own policy, its history
// keeping the router's routed row with the words why, which the
// operator's read shows. The router, never told its policy was not
// taken, answers the operator's query that it will route the record now
// (Routes): that is not believed of a policy not taken. The operator's
// answer is taken at once, applied, the router not told again; ana is
// never asked about.
func TestACountingPolicyFromARouterWithNoSinceIsNotTaken(t *testing.T) {
	for _, c := range sinceForms()[1:] {
		t.Run(c.name, func(t *testing.T) {
			f := &policyFixture{newDecideFixtureRouted(t, nil, c.router), c.fr}
			c.fr.SetPolicy("dr-1", groupsPolicy("exttest-r1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
			c.fr.SetRoute("dr-1", ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ana"}}, Rule: "architecture decisions"})
			did := f.open(t, "r1")
			want := "its router's policy asks for 2 approvals, and its router does not confirm an approval since its time: the operator answers it under the server's own policy"
			until(t, did+"'s routed row saying why", func() bool {
				for _, h := range f.record(t, did).History {
					if h.Kind == ledger.HistoryRouted && h.By == "router:switchman" && strings.Contains(h.Detail, want) {
						return true
					}
				}
				return false
			})
			rec := f.record(t, did)
			if ledger.RouterPolicy(rec.Policy) || rec.State != ledger.DecisionOpen {
				t.Fatalf("the record after a counting policy: %s %s", rec.State, rec.Policy)
			}
			read := f.expect(t, http.MethodGet, "/api/decisions/"+did, nil, http.StatusOK, "")
			if !strings.Contains(mustJSON(read), want) {
				t.Fatalf("the operator's read does not say why: %.600s", mustJSON(read))
			}
			c.fr.SetConfirm(did, routesStill)
			told := routerToldOf(c.fr, did)
			f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusCreated, "")
			f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
			if n := routerToldOf(c.fr, did); n != told {
				t.Fatalf("the router was told %s %d times more", did, n-told)
			}
			if all, _ := sinceQueries(c.fr, did, "ana"); all != 0 {
				t.Fatalf("the router was asked about ana %d times", all)
			}
		})
	}
}

// routesStill is a router that holds its route of a record as answered and
// not written: it will route the record now (Routes) whenever it is asked
// about it under the server's own policy.
func routesStill(q ext.ConfirmQuery) ext.Confirmation {
	if q.Revision == "local/1" && q.Subject == "" {
		return ext.Confirmation{InForce: true, Routes: true}
	}
	return ext.Confirmation{InForce: true}
}

// routerToldOf counts the times fr was told the record did.
func routerToldOf(fr *exttest.FakeRouter, did string) int {
	n := 0
	for _, d := range fr.Decisions() {
		if d.Request == did {
			n++
		}
	}
	return n
}

// A held answer to a record whose counting policy was not taken, from a
// router that answers no Since and still says it will route the record
// now (Routes): recorded at once, the operator's, applied, the router not
// told again.
func TestAHeldAnswerToACountingPolicyNotTakenIsTakenAtOnce(t *testing.T) {
	fr := exttest.NewFakeRouterNoSince("switchman")
	fr.SetPolicy("dr-1", groupsPolicy("exttest-r1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
	f := newHeldFixtureRouted(t, fr)
	did := f.openLocal(t, "q1")
	until(t, did+"'s routed row with its revision", func() bool {
		for _, h := range f.rec(t, did).History {
			if h.Kind == ledger.HistoryRouted && h.By == "router:switchman" && strings.Contains(h.Detail, `"revision":"exttest-r1"`) {
				return true
			}
		}
		return false
	})
	fr.SetConfirm(did, routesStill)
	told := routerToldOf(fr, did)
	out := f.hand(t, held(did, "h1", "ana", 0, f.rec(t, did).Rev))
	if out.Status != ext.AnswerRecorded || out.Repeat {
		t.Fatalf("the held answer: %+v", out)
	}
	until(t, did+" applied", func() bool { return f.rec(t, did).State == ledger.DecisionApplied })
	if n := routerToldOf(fr, did); n != told {
		t.Fatalf("the router was told %s %d times more", did, n-told)
	}
}

// countingPolicyText is a router's policy of two groups, ana in
// architecture and cam in security, as a record keeps it.
const countingPolicyText = `{"revision":"r1","approver":"router","quorum":1,"authority":[{"kind":"operator"}],"router":"switchman","scope":"s1",` +
	`"pinned":{"rule":"r-1","groups":[{"role":"architecture","quorum":1},{"role":"security","quorum":1}],"override":"operator","notify":"now"},` +
	`"groups":[{"role":"architecture","label":"architecture owner","quorum":1,"rule":"r-1","eligible":[{"subject":"ana","name":"Ana","via":"role","ref":"ref-1"}]},` +
	`{"role":"security","label":"security owner","quorum":1,"rule":"r-1","eligible":[{"subject":"cam","name":"Cam","via":"role","ref":"ref-2"}]}],` +
	`"override":"operator","routedAt":"2026-11-19T09:00:00Z","spare":true,"fallback":"local/1"}`

// A record that keeps a policy counting approvals (written while its
// server's router answered a Since), on a server whose router answers
// none: its read is not in force, saying why; ana, whom it names and the
// router confirms, neither reads it nor answers it; the operator answers
// it plainly, applied.
func TestAKeptCountingPolicyIsNotInForceWithARouterWithNoSince(t *testing.T) {
	fr := exttest.NewFakeRouterNoSince("switchman")
	f := &policyFixture{newDecideFixtureRouted(t, nil, fr), fr}
	did := f.open(t, "r1")
	until(t, did+" told to the router, its opening window closed", func() bool {
		return len(fr.Decisions()) > 0 && !f.srv.router.windows.in(did)
	})
	n, _ := ledger.ParseDecisionID(did)
	rev := f.rev(t, did)
	if _, err := f.srv.ledger.Transition(t.Context(), f.run, func(tx *ledger.Tx) error {
		ok, err := tx.PutPolicy(n, countingPolicyText, rev)
		if err == nil && !ok {
			err = errors.New("the policy was not written")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.fr.SetConfirm(did, func(q ext.ConfirmQuery) ext.Confirmation {
		if q.Subject == "ana" {
			return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}
		}
		return ext.Confirmation{InForce: true}
	})
	why := "it asks for 2 approvals, and its router does not confirm an approval since its time"
	if now := f.policyNowOf(t, adminToken, did); now == nil || now.InForce || now.Why != why {
		t.Fatalf("the operator's read: %+v", now)
	}
	if status, data := f.readAs(t, passAna, did); status != http.StatusNotFound {
		t.Fatalf("ana's read: %d %.300s", status, data)
	}
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did)); status == http.StatusCreated {
		t.Fatalf("ana's answer was taken: %s", data)
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), false, ""), http.StatusCreated, "")
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	for _, r := range f.record(t, did).Responses {
		if r.Status != ledger.ResponseAccepted {
			continue
		}
		if rc := f.receiptOf(t, did, r.K); r.Actor == "user:ana" || rc.Policy != "local/1" || rc.PolicyWhy != why {
			t.Fatalf("the accepted answer: %s %+v", r.Actor, rc)
		}
	}
}
