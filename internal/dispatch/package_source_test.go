package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The package's version 2 (Program item IN1a, docs/plans/tracker-linking.md,
// "The package, version 2"): the ticket's linked item as its source, its
// steps to reproduce when the revision hands them over, pinned at the
// proposal, and the links the revision names as evidence.

// sourceTicket is a store with a project CND, a ticket imported from
// GitHub's #612 (GitHub owning its title and description, its steps to
// reproduce found), and an external link of it with a note; the ticket's
// key and the link's id.
func sourceTicket(t *testing.T) (s *Store, key, link string) {
	t.Helper()
	s = linkedStore(t)
	newProject(t, s, "CND")
	key = mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "The login loops", "It loops.\n\n### Steps to reproduce\n\n1. Log in\n2. Loop"))
	rc, err := addLink(s, "link1", key, NewLink{Kind: LinkExternal, URL: "https://example.invalid/report/7", Note: "the first report, " + strings.Repeat("é", 300)})
	want(t, rc, err, StatusDone, "")
	return s, key, rc.ID
}

// refused fails the test unless the receipt is the refusal with the code
// and words that hold every part given.
func refused(t *testing.T, rc Receipt, err error, code string, words ...string) {
	t.Helper()
	want(t, rc, err, StatusFailed, code)
	for _, w := range words {
		if !strings.Contains(rc.Error, w) {
			t.Fatalf("the refusal %q does not say %q", rc.Error, w)
		}
	}
}

// A revision that hands over the steps to reproduce and an evidence link
// is launched with a package of version 2: its source the linked issue
// as the ticket holds it (its page, reference, node id, state, author,
// the owners, the snapshot's SHA-256, when it was read) with the steps,
// and its evidence the link's page and note cut to 200 characters. The
// body pins the steps' identity and SHA-256. A revision handing over
// neither has the source without the steps and no evidence; a native
// ticket's package has no source. A chosen link removed since is
// stale_plan at the approval and at the launch.
func TestPackageVersionTwo(t *testing.T) {
	ctx := context.Background()
	s, key, link := sourceTicket(t)
	tk := ticketOf(t, s, key)
	sn, _ := ParseSnapshot(string(tk.ExternalSnapshot))
	req := PlanRequest{Crew: "crew", Source: &PlanSourceRequest{Repro: true, Evidence: []string{link}}}
	body := testPlanBody("crew")
	body.Source = &PlanSource{Repro: &PlanRepro{}, Evidence: []string{link}}
	plan := approvedPlan(t, s, key, req, body, "go")
	var kept PlanBody
	decodeBody(t, planOf(t, s, plan).Body, &kept)
	if kept.Source == nil || *kept.Source.Repro != (PlanRepro{Identity: tk.ExternalID, SHA256: sn.ReproSha256}) || len(kept.Source.Evidence) != 1 || sn.ReproSha256 != sha256Hex(sn.Repro) {
		t.Fatalf("the body's source: %+v", kept.Source)
	}
	v := mustLaunch(t, s, "l1", key, plan, "")
	p := packageOf(t, v)
	view, err := s.Ticket(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	src := p.Source
	switch {
	case p.Version != 2 || src == nil:
		t.Fatalf("the package: version %d, source %+v", p.Version, src)
	case src.System != "github" || src.Host != "github.com" || src.URL != tk.ExternalURL || src.Ref != "rocksolidlabs/conductor#612" || src.NodeID != "I_1" ||
		src.State != "open" || src.Author != "bob" || !src.TitleFromGitHub || !src.DescriptionFromGitHub || src.Repro != "1. Log in\n2. Loop" ||
		src.SnapshotSha256 != sha256Hex(string(tk.ExternalSnapshot)) || view.ExternalRead == nil || !src.ReadAt.Equal(view.ExternalRead.ReadAt):
		t.Fatalf("the package's source: %+v", src)
	}
	if len(p.Evidence) != 1 || p.Evidence[0].URL != "https://example.invalid/report/7" || len([]rune(p.Evidence[0].Note)) != MaxEvidenceNote ||
		!strings.HasSuffix(p.Evidence[0].Note, "é…") {
		t.Fatalf("the package's evidence: %+v", p.Evidence)
	}

	// Neither handed over: the source without the steps, no evidence.
	bare := mustLaunch(t, s, "l2", key, simplePlan(t, s, key), "")
	if p := packageOf(t, bare); p.Source == nil || p.Source.Repro != "" || p.Evidence == nil || len(p.Evidence) != 0 || !strings.Contains(string(bare.Package), `"evidence":[]`) {
		t.Fatalf("the package handing nothing over: %+v %+v", p.Source, p.Evidence)
	}
	// A native ticket: no source.
	_, native := makeTicket(t, s, NewTicket{Project: "CND", Title: "Native"})
	if nv := mustLaunch(t, s, "l3", native, simplePlan(t, s, native), ""); packageOf(t, nv).Source != nil || !strings.Contains(string(nv.Package), `"source":null`) {
		t.Fatalf("a native ticket's package: %s", nv.Package)
	}

	// A chosen link removed: the approval and the launch are stale_plan.
	again := approvedPlan(t, s, key, req, body, "go")
	rc, err := s.RemoveLink(ctx, testOp("rm"), key, link)
	want(t, rc, err, StatusDone, "")
	rc, err = s.Launch(ctx, testOp("l4"), key, again, "")
	refused(t, rc, err, RefusalStalePlan, "the evidence link "+link+" was removed since revision")
	rc, err = s.ApprovePlan(ctx, adminOp("a4"), again, 0, "")
	refused(t, rc, err, RefusalStalePlan, "the evidence link "+link+" was removed")
	if ids, stale := approvedOf(t, s, key); len(ids) != 1 || ids[0] != again || stale[0] == nil || stale[0].Remedy != RemedyRevise {
		t.Fatalf("the approved revision's staleness: %v %+v", ids, stale)
	}

	// What a proposal may not hand over.
	_, other := makeTicket(t, s, NewTicket{Project: "CND", Title: "Other"})
	links := make([]string, 0, MaxPackageEvidence+1)
	for i := range MaxPackageEvidence + 1 {
		rc, err := addLink(s, opID("e", i), key, NewLink{Kind: LinkDelivery, URL: "https://example.invalid/pull/" + opID("", i)})
		want(t, rc, err, StatusDone, "")
		links = append(links, rc.ID)
	}
	for i, c := range []struct {
		name, key, code string
		src             *PlanSource
	}{
		{"steps of a native ticket", native, RefusalInvalid, &PlanSource{Repro: &PlanRepro{}}},
		{"another ticket's link", other, RefusalNotFound, &PlanSource{Evidence: []string{links[0]}}},
		{"a removed link", key, RefusalNotFound, &PlanSource{Evidence: []string{link}}},
		{"a link twice", key, RefusalInvalid, &PlanSource{Evidence: []string{links[0], links[0]}}},
		{"past the bound", key, RefusalBound, &PlanSource{Evidence: links}},
		{"a pin not the snapshot's", key, RefusalStalePlan, &PlanSource{Repro: &PlanRepro{Identity: tk.ExternalID, SHA256: strings.Repeat("0", 64)}}},
	} {
		b := testPlanBody("crew")
		b.Source = c.src
		rc, err := s.ProposePlan(ctx, testOp(opID("bad", i)), c.key, PlanRequest{Crew: "crew"}, b)
		if err != nil || rc.Refusal != c.code {
			t.Errorf("%s: %+v %v, want %s", c.name, rc, err, c.code)
		}
	}
	// Eight are taken; an empty source is none.
	b := testPlanBody("crew")
	b.Source = &PlanSource{Evidence: links[:MaxPackageEvidence]}
	propose(t, s, "eight", key, PlanRequest{Crew: "crew"}, b)
	b.Source = &PlanSource{Evidence: []string{}}
	decodeBody(t, planOf(t, s, propose(t, s, "none", key, PlanRequest{Crew: "crew"}, b)).Body, &kept)
	if kept.Source != nil {
		t.Fatalf("an empty source is kept: %+v", kept.Source)
	}
}

// decodeBody decodes a revision's body strictly.
func decodeBody(t *testing.T, raw []byte, b *PlanBody) {
	t.Helper()
	*b = PlanBody{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(b); err != nil {
		t.Fatalf("the body: %v", err)
	}
}

// A revision that pins the steps to reproduce of a ticket whose
// description Dispatch owns (the ticket's intent does not move when
// GitHub's text does) goes stale when a refresh brings in edited steps:
// the approval is stale with its remedy a new revision, approving it
// again and launching it are stale_plan; a new revision pins the new
// steps and launches them. The issue unlinked, or another linked, is the
// same.
func TestRefreshReproMakesApprovalStale(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Fix the login loop", Description: "Ours."})
	rc, err := s.LinkExternal(ctx, testOp("link"), LinkArgs{Key: key, URL: "https://github.com/rocksolidlabs/conductor/issues/612"}, ghRead("I_1", 0, "T", "B"))
	want(t, rc, err, StatusDone, "")
	withRepro := func(id string) string {
		body := testPlanBody("crew")
		body.Source = &PlanSource{Repro: &PlanRepro{}}
		return approvedPlan(t, s, key, PlanRequest{Crew: "crew", Source: &PlanSourceRequest{Repro: true}}, body, "go")
	}
	plan := withRepro("p1")
	read := ghRead("I_1", 5, "T", "B")
	read.Snapshot.Repro = "1. Log in\n2. Loop twice"
	rc, err = s.RefreshExternal(ctx, testOp("refresh"), key, read)
	want(t, rc, err, StatusDone, "")
	if tk := ticketOf(t, s, key); tk.Description != "Ours." {
		t.Fatalf("a refresh wrote the description Dispatch owns: %q", tk.Description)
	}
	if ids, stale := approvedOf(t, s, key); len(ids) != 1 || stale[0] == nil || stale[0].Remedy != RemedyRevise ||
		!strings.Contains(stale[0].Words, "the steps to reproduce changed since revision 1 was proposed") {
		t.Fatalf("the approval after the refresh: %v %+v", ids, stale)
	}
	rc, err = s.Launch(ctx, testOp("l1"), key, plan, "")
	refused(t, rc, err, RefusalStalePlan, "the steps to reproduce changed since revision 1 was proposed: revise it")
	rc, err = s.ApprovePlan(ctx, adminOp("again"), plan, 0, "")
	refused(t, rc, err, RefusalStalePlan, "the steps to reproduce changed")

	// A new revision pins the new steps and launches them.
	fresh := withRepro("p2")
	if p := packageOf(t, mustLaunch(t, s, "l2", key, fresh, "")); p.Source == nil || p.Source.Repro != "1. Log in\n2. Loop twice" || p.Source.DescriptionFromGitHub {
		t.Fatalf("the new revision's package: %+v", p.Source)
	}

	// Unlinked: the same.
	rc, err = s.UnlinkExternal(ctx, testOp("unlink"), key)
	want(t, rc, err, StatusDone, "")
	rc, err = s.Launch(ctx, testOp("l3"), key, fresh, "")
	refused(t, rc, err, RefusalStalePlan, "the steps to reproduce changed since revision 2 was proposed")
	if _, stale := approvedOf(t, s, key); stale[0] == nil || stale[0].Remedy != RemedyRevise {
		t.Fatalf("the approval after the unlink: %+v", stale)
	}
	// Another linked: the same, its steps those of another item.
	rc, err = s.LinkExternal(ctx, testOp("relink"), LinkArgs{Key: key, URL: "https://github.com/rocksolidlabs/conductor/issues/613"}, ghRead("I_2", 9, "T", "B"))
	want(t, rc, err, StatusDone, "")
	rc, err = s.Launch(ctx, testOp("l4"), key, fresh, "")
	refused(t, rc, err, RefusalStalePlan, "the steps to reproduce changed")
}

// Evidence is the revision's whatever the ticket's linked issue: a native
// ticket with only an external link launches with the evidence and no
// source; a linked ticket whose issue is unlinked before the launch keeps
// the evidence, its package without a source.
func TestEvidenceWithoutCounterpart(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Native"})
	rc, err := addLink(s, "l", key, NewLink{Kind: LinkExternal, URL: "http://example.invalid/log", Note: "the log"})
	want(t, rc, err, StatusDone, "")
	evidence := func(link string) string {
		body := testPlanBody("crew")
		body.Source = &PlanSource{Evidence: []string{link}}
		return approvedPlan(t, s, key, PlanRequest{Crew: "crew", Source: &PlanSourceRequest{Evidence: []string{link}}}, body, "go")
	}
	p := packageOf(t, mustLaunch(t, s, "l1", key, evidence(rc.ID), ""))
	if p.Source != nil || len(p.Evidence) != 1 || p.Evidence[0] != (PackageEvidence{URL: "http://example.invalid/log", Note: "the log"}) {
		t.Fatalf("a native ticket's package: %+v %+v", p.Source, p.Evidence)
	}
	rc, err = s.LinkExternal(ctx, testOp("link"), LinkArgs{Key: key, URL: "https://github.com/rocksolidlabs/conductor/issues/612"}, ghRead("I_1", 0, "T", "B"))
	want(t, rc, err, StatusDone, "")
	delivery, err := addLink(s, "d", key, NewLink{Kind: LinkDelivery, URL: "https://github.com/rocksolidlabs/conductor/pull/9"})
	want(t, delivery, err, StatusDone, "")
	plan := evidence(delivery.ID)
	rc, err = s.UnlinkExternal(ctx, testOp("unlink"), key)
	want(t, rc, err, StatusDone, "")
	p = packageOf(t, mustLaunch(t, s, "l2", key, plan, ""))
	if p.Source != nil || len(p.Evidence) != 1 || p.Evidence[0].URL != "https://github.com/rocksolidlabs/conductor/pull/9" {
		t.Fatalf("the package after the unlink: %+v %+v", p.Source, p.Evidence)
	}
}
