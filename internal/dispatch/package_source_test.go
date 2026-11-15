package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// pinOf is the pin of the ticket's steps to reproduce as a proposal shows
// them: the linked item's identity and the steps' SHA-256 now.
func pinOf(t *testing.T, s *Store, key string) *PlanRepro {
	t.Helper()
	tk := ticketOf(t, s, key)
	sn, ok := ParseSnapshot(string(tk.ExternalSnapshot))
	if !ok || sn.ReproSha256 == "" {
		t.Fatalf("%s has no steps to reproduce to pin", key)
	}
	return &PlanRepro{Identity: tk.ExternalID, SHA256: sn.ReproSha256}
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
	req := PlanRequest{Crew: "crew"}
	body := testPlanBody("crew")
	body.Source = &PlanSource{Repro: pinOf(t, s, key), Evidence: []string{link}}
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

	// Neither handed over: the source without the steps, the evidence left
	// out.
	bare := mustLaunch(t, s, "l2", key, simplePlan(t, s, key), "")
	if p := packageOf(t, bare); p.Source == nil || p.Source.Repro != "" || p.Evidence != nil || strings.Contains(string(bare.Package), `"evidence"`) {
		t.Fatalf("the package handing nothing over: %+v %+v", p.Source, p.Evidence)
	}
	// A native ticket: the source left out too.
	_, native := makeTicket(t, s, NewTicket{Project: "CND", Title: "Native"})
	if nv := mustLaunch(t, s, "l3", native, simplePlan(t, s, native), ""); packageOf(t, nv).Source != nil || strings.Contains(string(nv.Package), `"source"`) {
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
		{"steps of a native ticket", native, RefusalInvalid, &PlanSource{Repro: &PlanRepro{Identity: "github:github.com:I_1", SHA256: sn.ReproSha256}}},
		{"an empty pin", key, RefusalInvalid, &PlanSource{Repro: &PlanRepro{}}},
		{"a pin without its SHA-256", key, RefusalInvalid, &PlanSource{Repro: &PlanRepro{Identity: tk.ExternalID}}},
		{"a pin without its identity", key, RefusalInvalid, &PlanSource{Repro: &PlanRepro{SHA256: sn.ReproSha256}}},
		{"another ticket's link", other, RefusalNotFound, &PlanSource{Evidence: []string{links[0]}}},
		{"a removed link", key, RefusalNotFound, &PlanSource{Evidence: []string{link}}},
		{"a link twice", key, RefusalInvalid, &PlanSource{Evidence: []string{links[0], links[0]}}},
		{"past the bound", key, RefusalBound, &PlanSource{Evidence: links}},
		{"a pin not the snapshot's", key, RefusalStalePlan, &PlanSource{Repro: &PlanRepro{Identity: tk.ExternalID, SHA256: strings.Repeat("0", 64)}}},
		{"the snapshot's hash with another item's identity", key, RefusalStalePlan, &PlanSource{Repro: &PlanRepro{Identity: "github:github.com:I_9", SHA256: sn.ReproSha256}}},
	} {
		b := testPlanBody("crew")
		b.Source = c.src
		rc, err := s.ProposePlan(ctx, testOp(opID("bad", i)), c.key, PlanRequest{Crew: "crew"}, b)
		if err != nil || rc.Refusal != c.code {
			t.Errorf("%s: %+v %v, want %s", c.name, rc, err, c.code)
		}
		if c.key == key && c.code == RefusalInvalid && c.src.Repro != nil && !strings.Contains(rc.Error, "by their pin: the linked item's identity and the SHA-256 of the steps shown") {
			t.Errorf("%s: the refusal does not say what the pin is: %q", c.name, rc.Error)
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
		body.Source = &PlanSource{Repro: pinOf(t, s, key)}
		return approvedPlan(t, s, key, PlanRequest{Crew: "crew"}, body, "go")
	}
	plan := withRepro("p1")
	shown := pinOf(t, s, key)
	read := ghRead("I_1", 5, "T", "B")
	read.Snapshot.Repro = "1. Log in\n2. Loop twice"
	rc, err = s.RefreshExternal(ctx, testOp("refresh"), key, read)
	want(t, rc, err, StatusDone, "")
	// A proposal that pins the steps it showed before the refresh is
	// stale_plan.
	b := testPlanBody("crew")
	b.Source = &PlanSource{Repro: shown}
	rc, err = s.ProposePlan(ctx, testOp("shown-before"), key, PlanRequest{Crew: "crew"}, b)
	refused(t, rc, err, RefusalStalePlan, "the steps to reproduce changed since they were shown")
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

// A title and a description a read of the linked issue wrote are flagged
// as GitHub's by their text, whoever owns the field: imported, a revision
// approved, both fields adopted for Dispatch with no edit, the launch is
// taken on that approval and its package flags both, their text byte for
// byte the import's; a refresh that changes GitHub's text after the
// adoption leaves the adopted text flagged. The description edited in
// Dispatch makes the approval stale as an edit does, and once approved
// again it is written as Dispatch's, the title still GitHub's.
func TestAdoptedTextStaysGitHubs(t *testing.T) {
	ctx := context.Background()
	s := linkedStore(t)
	newProject(t, s, "CND")
	key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "SCRATCH-TITLE-FROM-OUTSIDE", "SCRATCH-LINE-FROM-OUTSIDE"))
	before := ticketOf(t, s, key)
	plan := simplePlan(t, s, key)
	for _, field := range []string{FieldTitle, FieldDescription} {
		rc, err := s.SetAuthority(ctx, testOp("adopt-"+field), AuthorityArgs{Key: key, Field: field, Owner: OwnerDispatch}, nil)
		want(t, rc, err, StatusDone, "")
	}
	if a := ParseAuthority(string(ticketOf(t, s, key).Authority)); a.Title != "" || a.Description != "" {
		t.Fatalf("the fields are not Dispatch's: %+v", a)
	}
	p := packageOf(t, mustLaunch(t, s, "l1", key, plan, ""))
	if p.Source == nil || !p.Source.TitleFromGitHub || !p.Source.DescriptionFromGitHub || p.Ticket.Title != before.Title || p.Ticket.Description != before.Description {
		t.Fatalf("the package after the adoption: %+v, %q %q", p.Source, p.Ticket.Title, p.Ticket.Description)
	}
	// GitHub's text changes; the adopted text stays as it was, and GitHub's.
	rc, err := s.RefreshExternal(ctx, testOp("refresh"), key, ghRead("I_1", 5, "SCRATCH-TITLE-EDITED", "SCRATCH-LINE-EDITED"))
	want(t, rc, err, StatusDone, "")
	p = packageOf(t, mustLaunch(t, s, "l2", key, plan, ""))
	if !p.Source.TitleFromGitHub || !p.Source.DescriptionFromGitHub || p.Ticket.Description != before.Description {
		t.Fatalf("the package after a refresh: %+v %q", p.Source, p.Ticket.Description)
	}
	// Edited in Dispatch: stale as an edit makes it, then Dispatch's.
	mustEdit(t, s, "edit", key, TicketEdit{Set: TicketSet{Description: ptr("Ours now.")}})
	rc, err = s.Launch(ctx, testOp("l3"), key, plan, "")
	refused(t, rc, err, RefusalStalePlan, "approve it again or revise it")
	approve(t, s, "again", plan, "go")
	p = packageOf(t, mustLaunch(t, s, "l4", key, plan, ""))
	if !p.Source.TitleFromGitHub || p.Source.DescriptionFromGitHub || p.Ticket.Description != "Ours now." {
		t.Fatalf("the package after the edit: %+v %q", p.Source, p.Ticket.Description)
	}
}

// A text a read wrote stays quoted whoever owns the field: a ticket
// imported, its fields adopted for Dispatch or the ticket unlinked, each
// with no change of the text, before the revision's approval or between
// the approval and the launch: the launch is taken on that approval, and
// its package flags the title and the description as quoted, their text
// as a launch before either made them. Adopted, the source's words
// (system, reference, account, page, snapshot) are that launch's;
// unlinked, the source says so and holds nothing else.
func TestAnAdoptionOrAnUnlinkKeepsTheQuote(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name             string
		unlink, approved bool
	}{{"adopted before the approval", false, false}, {"adopted between the approval and the launch", false, true},
		{"unlinked before the approval", true, false}, {"unlinked between the approval and the launch", true, true}} {
		t.Run(c.name, func(t *testing.T) {
			s := linkedStore(t)
			newProject(t, s, "CND")
			key := mustImport(t, s, "imp", "CND", ghRead("I_1", 0, "SCRATCH-TITLE-FROM-OUTSIDE", "SCRATCH-LINE-FROM-OUTSIDE"))
			was := packageOf(t, mustLaunch(t, s, "l0", key, simplePlan(t, s, key), ""))
			act := func() {
				if c.unlink {
					rc, err := s.UnlinkExternal(ctx, testOp("unlink"), key)
					want(t, rc, err, StatusDone, "")
					return
				}
				for _, field := range []string{FieldTitle, FieldDescription} {
					rc, err := s.SetAuthority(ctx, testOp("adopt-"+field), AuthorityArgs{Key: key, Field: field, Owner: OwnerDispatch}, nil)
					want(t, rc, err, StatusDone, "")
				}
			}
			if !c.approved {
				act()
			}
			plan := simplePlan(t, s, key)
			if c.approved {
				act()
			}
			p := packageOf(t, mustLaunch(t, s, "l1", key, plan, ""))
			src, before := p.Source, was.Source
			if c.unlink {
				before = &PackageSource{Unlinked: true, TitleFromGitHub: true, DescriptionFromGitHub: true}
			}
			if src == nil || *src != *before || p.Ticket.Title != was.Ticket.Title || p.Ticket.Description != was.Ticket.Description {
				t.Fatalf("the package: %+v\nwant: %+v", src, before)
			}
		})
	}
}

// A related ticket's title a read of its linked item wrote last is
// withheld in the package (the parent's, a blocker's), a native one's
// kept, one a person has since edited kept; a run's default label is the
// key alone while a read wrote the ticket's title, its key and title once
// a person has edited it.
func TestRelatedTitlesAndTheLabel(t *testing.T) {
	s := linkedStore(t)
	newProject(t, s, "CND")
	parent := mustImport(t, s, "imp-parent", "CND", ghRead("I_1", 0, "SCRATCH-PARENT-FROM-OUTSIDE", "b"))
	blocker := mustImport(t, s, "imp-blocker", "CND", ghRead("I_2", 0, "SCRATCH-BLOCKER-FROM-OUTSIDE", "b"))
	_, native := makeTicket(t, s, NewTicket{Project: "CND", Title: "Native blocker"})
	_, key := makeTicket(t, s, NewTicket{Project: "CND", Title: "Child", Parent: parent})
	for i, b := range []string{blocker, native} {
		rc, err := relate(s, opID("rel", i), b, RelationBlocks, key)
		want(t, rc, err, StatusDone, "")
	}
	p := packageOf(t, mustLaunch(t, s, "l1", key, simplePlan(t, s, key), ""))
	titles := map[string]string{}
	for _, b := range p.Ticket.Blockers {
		titles[b.Key] = b.Title
	}
	if p.Ticket.Parent == nil || p.Ticket.Parent.Title != WithheldTitle || titles[blocker] != WithheldTitle || titles[native] != "Native blocker" {
		t.Fatalf("the related titles: %+v %v", p.Ticket.Parent, titles)
	}
	if v := mustLaunch(t, s, "l2", parent, simplePlan(t, s, parent), ""); v.Label != parent {
		t.Fatalf("the label of a ticket whose title a read wrote: %q", v.Label)
	}
	rc, err := s.SetAuthority(context.Background(), testOp("adopt"), AuthorityArgs{Key: parent, Field: FieldTitle, Owner: OwnerDispatch}, nil)
	want(t, rc, err, StatusDone, "")
	if v := mustLaunch(t, s, "l2a", parent, simplePlan(t, s, parent), ""); v.Label != parent {
		t.Fatalf("the label of a title adopted with no edit: %q", v.Label)
	}
	mustEdit(t, s, "edit", parent, TicketEdit{Set: TicketSet{Title: ptr("Ours now")}})
	if v := mustLaunch(t, s, "l3", parent, simplePlan(t, s, parent), ""); v.Label != parent+" Ours now" {
		t.Fatalf("the label once a person edited the title: %q", v.Label)
	}
	if p := packageOf(t, mustLaunch(t, s, "l4", key, simplePlan(t, s, key), "")); p.Ticket.Parent.Title != "Ours now" {
		t.Fatalf("the parent's title once a person edited it: %q", p.Ticket.Parent.Title)
	}
}

// The bounds are their values, and each is the bound: a source or
// evidence of exactly its bound as encoded is taken, one byte more
// refused.
func TestThePartBoundsAreExact(t *testing.T) {
	if MaxPackage != 640<<10 || MaxPackageSource != 24<<10 || MaxPackageEvidenceJSON != 48<<10 {
		t.Fatalf("the bounds: %d %d %d", MaxPackage, MaxPackageSource, MaxPackageEvidenceJSON)
	}
	size := func(v any) int {
		b, err := encodeValue(v)
		if err != nil {
			t.Fatal(err)
		}
		return len(b)
	}
	src := &PackageSource{System: "github", URL: "https://github.com/acme/widgets/issues/612", Ref: "acme/widgets#612", State: "open", SnapshotSha256: strings.Repeat("5", 64), Repro: "x"}
	src.Repro += strings.Repeat("r", MaxPackageSource-size(src))
	evidence := []PackageEvidence{{URL: "https://example.invalid/log", Note: "x"}}
	evidence[0].Note += strings.Repeat("n", MaxPackageEvidenceJSON-size(evidence))
	if size(src) != MaxPackageSource || size(evidence) != MaxPackageEvidenceJSON {
		t.Fatalf("the parts are %d and %d bytes", size(src), size(evidence))
	}
	if err := checkParts(Package{Source: src, Evidence: evidence}); err != nil {
		t.Fatalf("the parts at their bounds: %v", err)
	}
	src.Repro += "r"
	evidence[0].Note += "n"
	var ref *Refusal
	for name, p := range map[string]Package{"the source": {Source: src}, "the evidence": {Evidence: evidence}} {
		if err := checkParts(p); !errors.As(err, &ref) || ref.Code != RefusalBound {
			t.Errorf("%s one byte past its bound: %v", name, err)
		}
	}
}

// The source and the evidence are held to the rules the engine reads them
// by before a launch is committed: a part of characters the package writes
// long (as a merged replica's snapshot or links could hold them) past its
// bound as written is refused bound, in words naming it; the same part
// written short is taken; a page that is not an http or https URL and a
// state of the source's own are invalid.
func TestThePartsAreCheckedAsTheEngineReadsThem(t *testing.T) {
	nul := string(rune(0))
	src := func() *PackageSource {
		return &PackageSource{System: "github", Host: "github.com", URL: "HTTPS://github.com/acme/widgets/issues/612", Ref: "acme/widgets#612", State: "open",
			SnapshotSha256: strings.Repeat("5", 64), Repro: strings.Repeat("r", MaxRepro)}
	}
	if err := checkParts(Package{Source: src()}); err != nil {
		t.Fatalf("the steps at their bound written short: %v", err)
	}
	long := src()
	long.Repro = strings.Repeat(nul, MaxRepro)
	var ref *Refusal
	if err := checkParts(Package{Source: long}); !errors.As(err, &ref) || ref.Code != RefusalBound ||
		!strings.HasPrefix(ref.Words, "the source (the linked item and its steps to reproduce) is 48.") || !strings.HasSuffix(ref.Words, "more than the 24.0 KiB a package holds") {
		t.Fatalf("the steps at their bound written long: %v", err)
	}
	evidence := func(note string) []PackageEvidence {
		var out []PackageEvidence
		for i := range MaxPackageEvidence {
			out = append(out, PackageEvidence{URL: fmt.Sprintf("http://example.invalid/%02d/", i) + strings.Repeat("a", MaxLinkURL-26), Note: note})
		}
		return out
	}
	if err := checkParts(Package{Evidence: evidence(strings.Repeat("n", MaxEvidenceNote))}); err != nil {
		t.Fatalf("the evidence written short: %v", err)
	}
	if err := checkParts(Package{Evidence: evidence(strings.Repeat(nul, MaxEvidenceNote) + strings.Repeat("\"", 2000))}); !errors.As(err, &ref) || ref.Code != RefusalBound ||
		!strings.HasPrefix(ref.Words, "the evidence is ") || !strings.HasSuffix(ref.Words, "more than the 48.0 KiB a package holds") {
		t.Fatalf("the evidence written long: %v", err)
	}
	for name, p := range map[string]Package{
		"a page not a web page":   {Source: func() *PackageSource { s := src(); s.URL = "ftp://github.com/x"; return s }()},
		"a state of its own":      {Source: func() *PackageSource { s := src(); s.State = "stale"; return s }()},
		"an evidence page of ftp": {Evidence: []PackageEvidence{{URL: "ftp://example.invalid/log"}}},
	} {
		if err := checkParts(p); !errors.As(err, &ref) || ref.Code != RefusalInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The launch refuses a package the engine would refuse, before it is
// committed: rows past what the store's own operations make (as a merged
// replica's could hold them), each past one of the package's bounds (the
// source as written, the evidence as written, the count of evidence
// links), are refused bound in words naming it, and the store holds no
// launch and so no run.
func TestTheLaunchRefusesWhatTheEngineWould(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, words string
		past        func(t *testing.T, s *Store, key string, body *PlanBody, links []string)
	}{
		{"the source", "the source (the linked item and its steps to reproduce) is 24.4 KiB as the package writes it, more than the 24.0 KiB a package holds", func(t *testing.T, s *Store, key string, body *PlanBody, _ []string) {
			tk := ticketOf(t, s, key)
			sn, _ := ParseSnapshot(string(tk.ExternalSnapshot))
			sn.Repro = strings.Repeat("r", MaxPackageSource)
			sn.ReproSha256 = sha256Hex(sn.Repro)
			raw, err := json.Marshal(sn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE tickets SET external_snapshot = ? WHERE id = ?", string(raw), tk.ID); err != nil {
				t.Fatal(err)
			}
			body.Source = &PlanSource{Repro: pinOf(t, s, key)}
		}},
		{"the evidence", "the evidence is 56.", func(t *testing.T, s *Store, _ string, body *PlanBody, links []string) {
			for _, id := range links[:MaxPackageEvidence] {
				if _, err := s.db.Exec("UPDATE links SET url = ? WHERE id = ?", "https://example.invalid/"+strings.Repeat("a", 7<<10), id); err != nil {
					t.Fatal(err)
				}
			}
			body.Source = &PlanSource{Evidence: links[:MaxPackageEvidence]}
		}},
		{"the count of evidence links", "the revision names 9 evidence links, more than the 8 a package holds", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, key, _ := sourceTicket(t)
			var links []string
			for i := range MaxPackageEvidence + 1 {
				rc, err := addLink(s, opID("ev", i), key, NewLink{Kind: LinkExternal, URL: "https://example.invalid/" + opID("", i)})
				want(t, rc, err, StatusDone, "")
				links = append(links, rc.ID)
			}
			body := testPlanBody("crew")
			if c.past != nil {
				c.past(t, s, key, &body, links)
			} else {
				body.Source = &PlanSource{Evidence: links[:MaxPackageEvidence]}
			}
			plan := approvedPlan(t, s, key, PlanRequest{Crew: "crew"}, body, "go")
			if c.past == nil {
				// Nine links named in the revision's body as the store keeps it.
				var kept PlanBody
				decodeBody(t, planOf(t, s, plan).Body, &kept)
				kept.Source.Evidence = links
				raw, err := encodeValue(kept)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec("UPDATE plans SET body = ? WHERE id = ?", raw, plan); err != nil {
					t.Fatal(err)
				}
			}
			rc, err := s.Launch(ctx, testOp("launch"), key, plan, "")
			refused(t, rc, err, RefusalBound, c.words)
			var n int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM launches WHERE ticket_id = ?", ticketOf(t, s, key).ID).Scan(&n); err != nil || n != 0 || rc.ID != "" {
				t.Fatalf("the store holds %d launches (%v), the receipt %+v", n, err, rc)
			}
		})
	}
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
		return approvedPlan(t, s, key, PlanRequest{Crew: "crew"}, body, "go")
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
