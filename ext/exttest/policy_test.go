package exttest

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// policyRouter answers, for every record it is told, the policy p: valid
// for the record the Router check tells it when p is.
type policyRouter struct {
	*FakeRouter
	p ext.RoutePolicy
}

func (r policyRouter) Route(ctx context.Context, d ext.Decision) (ext.Route, error) {
	rt, err := r.FakeRouter.Route(ctx, d)
	if d.Record != nil {
		rt.Policy = r.p.Clone()
	}
	return rt, err
}

// The Router check tells a record with what it says of itself, and a
// router that answers a policy valid for it passes; one that answers a
// policy not valid for it (its groups not the requirement's) is found.
func TestRouterContractWithAPolicy(t *testing.T) {
	n := NewNetwork()
	errs := record(t, func(rt T) {
		Router(rt, n, policyRouter{NewFakeRouter("policies"), SamplePolicy()}, SampleDecisionRequest())
	})
	if len(errs) != 0 {
		t.Fatalf("a router answering a valid policy failed the check:\n%s", strings.Join(errs, "\n"))
	}
	bad := SamplePolicy()
	bad.Groups[0].Role = "security"
	n = NewNetwork()
	errs = record(t, func(rt T) { Router(rt, n, policyRouter{NewFakeRouter("strays"), bad}, SampleDecisionRequest()) })
	if !said(errs, `exttest: Router "strays": Route answered a route not valid for its decision`, "requirement's role") {
		t.Fatalf("a router answering a policy not valid for its record was not found; it said:\n%s", strings.Join(errs, "\n"))
	}
}

// fetchingConfirmer asks its service for a record's policy the first time
// it is asked about it, on the call's path.
type fetchingConfirmer struct {
	*FakeRouter
	client *http.Client
	url    string
	mu     sync.Mutex
	seen   map[string]bool
}

func (r *fetchingConfirmer) Confirm(ctx context.Context, q ext.ConfirmQuery) (ext.Confirmation, error) {
	r.mu.Lock()
	seen := r.seen[q.Decision.Request+"/"+q.Subject]
	r.mu.Unlock()
	if !seen {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, r.url, nil)
		if err != nil {
			return ext.Confirmation{}, err
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return ext.Confirmation{}, err
		}
		resp.Body.Close()
		r.mu.Lock()
		r.seen[q.Decision.Request+"/"+q.Subject] = true
		r.mu.Unlock()
	}
	return ext.Confirmation{InForce: true, Groups: []string{"architecture"}}, nil
}

// invalidConfirmer answers groups with a policy not in force.
type invalidConfirmer struct{ *FakeRouter }

func (invalidConfirmer) Confirm(context.Context, ext.ConfirmQuery) (ext.Confirmation, error) {
	return ext.Confirmation{Groups: []string{"architecture"}}, nil
}

// The Confirmer check: the fake router, answering from what it holds,
// passes; a router that fetches on the call's path is found with the
// network blocked (article 8); one answering a confirmation that is not
// valid is found; and RunAll runs it for a router that confirms policies.
func TestConfirmerContract(t *testing.T) {
	n := NewNetwork()
	f := NewFakeRouter("confirms")
	if errs := record(t, func(rt T) { Confirmer(rt, n, f, SampleDecisionRequest()) }); len(errs) != 0 {
		t.Fatalf("the fake router failed the check:\n%s", strings.Join(errs, "\n"))
	}
	if len(f.Confirms()) == 0 {
		t.Fatal("the check asked nothing")
	}
	srv := keyService(t)
	n = NewNetwork()
	fetcher := &fetchingConfirmer{FakeRouter: NewFakeRouter("fetcher"), client: &http.Client{Transport: n.Transport()}, url: srv.URL, seen: map[string]bool{}}
	errs := record(t, func(rt T) { Confirmer(rt, n, fetcher, SampleDecisionRequest()) })
	if !said(errs, `exttest: Confirmer "fetcher": Confirm did not return within 1s with the network blocked`, "article 8") {
		t.Fatalf("a router fetching on the call's path was not found; it said:\n%s", strings.Join(errs, "\n"))
	}
	n = NewNetwork()
	errs = record(t, func(rt T) { Confirmer(rt, n, invalidConfirmer{NewFakeRouter("invalid")}, SampleDecisionRequest()) })
	if !said(errs, `exttest: Confirmer "invalid": Confirm answered a confirmation that is not valid`, "groups are an answer in force") {
		t.Fatalf("an invalid confirmation was not found; it said:\n%s", strings.Join(errs, "\n"))
	}
	n = NewNetwork()
	errs = record(t, func(rt T) { RunAll(rt, n, ext.Providers{Router: invalidConfirmer{NewFakeRouter("run-all")}}) })
	if !said(errs, `exttest: Confirmer "run-all"`) {
		t.Fatalf("RunAll did not run the Confirmer check; it said:\n%s", strings.Join(errs, "\n"))
	}
}

// sinceFlipper says it answers a query with a Since and then that it does
// not.
type sinceFlipper struct {
	*FakeRouter
	mu    sync.Mutex
	asked int
}

func (r *sinceFlipper) ConfirmsSince() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked++
	return r.asked == 1
}

// invalidSince says it answers a query with a Since and answers one with a
// confirmation that is not valid.
type invalidSince struct{ *FakeRouter }

func (r invalidSince) Confirm(ctx context.Context, q ext.ConfirmQuery) (ext.Confirmation, error) {
	if !q.Since.IsZero() {
		return ext.Confirmation{Groups: []string{"architecture"}}, nil
	}
	return r.FakeRouter.Confirm(ctx, q)
}

// The Confirmer check of a router's word on a query with a Since: the fake
// that answers one is asked one, in every phase; the fake that says it
// answers none, and the one that does not say, pass and are asked none;
// a router whose word changes is found, and one that says it answers one
// and answers it with a confirmation that is not valid is found.
func TestConfirmerContractWithASince(t *testing.T) {
	sinces := func(f *FakeRouter) int {
		n := 0
		for _, q := range f.Confirms() {
			if !q.Since.IsZero() {
				n++
			}
		}
		return n
	}
	f := NewFakeRouter("since")
	if errs := record(t, func(rt T) { Confirmer(rt, NewNetwork(), f, SampleDecisionRequest()) }); len(errs) != 0 {
		t.Fatalf("the fake that answers a Since failed the check:\n%s", strings.Join(errs, "\n"))
	}
	if got := sinces(f); got < 3 {
		t.Fatalf("the fake that answers a Since was asked %d queries with one, want one in each phase at least", got)
	}
	for _, name := range []string{"says-none", "does-not-say"} {
		f := NewFakeRouterNoSince(name)
		var router ext.DecisionRouter = f
		if name == "does-not-say" {
			router = WithoutSince(f)
		}
		if errs := record(t, func(rt T) { Confirmer(rt, NewNetwork(), router, SampleDecisionRequest()) }); len(errs) != 0 {
			t.Fatalf("the fake that %s failed the check:\n%s", name, strings.Join(errs, "\n"))
		}
		if len(f.Confirms()) == 0 || sinces(f) != 0 {
			t.Fatalf("the fake that %s was asked %v: want plain queries alone", name, f.Confirms())
		}
	}
	errs := record(t, func(rt T) {
		Confirmer(rt, NewNetwork(), &sinceFlipper{FakeRouter: NewFakeRouter("flipper")}, SampleDecisionRequest())
	})
	if !said(errs, `exttest: Confirmer "flipper": ConfirmsSince answered true and then false`) {
		t.Fatalf("a router whose word on a Since changed was not found; it said:\n%s", strings.Join(errs, "\n"))
	}
	errs = record(t, func(rt T) {
		Confirmer(rt, NewNetwork(), invalidSince{NewFakeRouter("bad-since")}, SampleDecisionRequest())
	})
	if !said(errs, `exttest: Confirmer "bad-since": Confirm answered a confirmation that is not valid`, "with a Since", "groups are an answer in force") {
		t.Fatalf("an invalid answer to a query with a Since was not found; it said:\n%s", strings.Join(errs, "\n"))
	}
}
