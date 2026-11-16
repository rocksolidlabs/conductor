package crew

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/quoted"
)

// A package as Dispatch's store builds it for a launch, read by the
// engine's reader (Program item IN1a): the store and the engine hold one
// set of rules, so a launch Dispatch commits is never one the engine
// refuses. These tests use the store's own operations; nothing is read
// from a tracker.

// storeOp is an operation of the actor test.
func storeOp(id string) dispatch.Op { return dispatch.Op{Actor: "test", ID: id, By: "Tester"} }

// taken takes a store operation's answer, failing the test unless its
// receipt is done.
func taken(t *testing.T, what string) func(dispatch.Receipt, error) dispatch.Receipt {
	return func(rc dispatch.Receipt, err error) dispatch.Receipt {
		t.Helper()
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("%s: %+v %v", what, rc, err)
		}
		return rc
	}
}

// itemRead is a read of the invented issue n of a tracker named github,
// in the space given, by the author given, with its steps to reproduce,
// its page on github.com.
func itemRead(n int, space, author, title, body string) dispatch.ExternalRead {
	return itemReadAt(fmt.Sprintf("https://github.com/acme/widgets/issues/%d", n), n, space, author, title, body)
}

// itemReadAt is itemRead with its page at the address given.
func itemReadAt(page string, n int, space, author, title, body string) dispatch.ExternalRead {
	return dispatch.ExternalRead{
		Snapshot: dispatch.ExternalSnapshot{V: dispatch.SnapshotVersion, System: "github", Host: "github.com", Space: space, Kind: dispatch.ExternalIssue,
			ID: fmt.Sprint(n), NodeID: fmt.Sprintf("I_%d", n), URL: page, Title: title, State: "open",
			Author: author, UpdatedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), Repro: "1. SCRATCH-STEP-FROM-OUTSIDE"},
		Body:      body,
		IssueETag: `"e1"`,
	}
}

// storeCrew is a saved crew as a revision's body holds it: two members
// and two tasks.
var storeCrew = json.RawMessage(`{"id":"crew","name":"Crew","cwd":"/work/widgets","where":"server","isolation":"worktree","yolo":false,` +
	`"members":[{"name":"dev","agentId":"claude"},{"name":"rev","agentId":"codex"}],` +
	`"tasks":[{"id":"build","member":"dev","kind":"work","instructions":"Build it"},{"id":"review","member":"rev","kind":"review","instructions":"Review it","needs":["build"]}],` +
	`"createdAt":"2026-10-30T11:00:00Z","updatedAt":"2026-10-30T12:00:00Z"}`)

// storeAgent is a catalog agent as a revision records it.
func storeAgent(id string) dispatch.PlanAgent {
	sum := strings.Repeat("a", 64)
	return dispatch.PlanAgent{ID: id, Command: []string{id}, StartArgs: []string{"--session-id", "{id}"}, EnvNames: []string{"A"}, EnvSHA256: sum, SHA256: sum}
}

// launchOf proposes a revision of the ticket handing over the evidence
// links given and, when evidence is not nil, its steps by their pin,
// approves it, runs between (after the approval, before the launch) and
// launches it: the package's bytes.
func launchOf(t *testing.T, s *dispatch.Store, key string, evidence []string, between func()) []byte {
	t.Helper()
	return launchAs(t, s, key, key, evidence, between)
}

// launchAs is launchOf under operation ids of the tag's.
func launchAs(t *testing.T, s *dispatch.Store, tag, key string, evidence []string, between func()) []byte {
	t.Helper()
	return launchViewAs(t, s, tag, key, evidence, between).Package
}

// launchViewAs is launchAs's launch as the store reads it: its label
// beside its package.
func launchViewAs(t *testing.T, s *dispatch.Store, tag, key string, evidence []string, between func()) dispatch.LaunchView {
	t.Helper()
	ctx := context.Background()
	v, err := s.Ticket(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	body := dispatch.PlanBody{Base: strings.Repeat("ab", 20), Crew: storeCrew, Agents: []dispatch.PlanAgent{storeAgent("claude"), storeAgent("codex")}}
	if evidence != nil {
		sn, _ := dispatch.ParseSnapshot(string(v.ExternalSnapshot))
		body.Source = &dispatch.PlanSource{Repro: &dispatch.PlanRepro{Identity: v.ExternalID, SHA256: sn.ReproSha256}, Evidence: evidence}
	}
	plan := taken(t, "propose")(s.ProposePlan(ctx, storeOp("p-"+tag), key, dispatch.PlanRequest{Crew: "crew"}, body))
	taken(t, "approve")(s.ApprovePlan(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: "a-" + tag, By: "Operator"}, plan.ID, 0, "go"))
	if between != nil {
		between()
	}
	rc := taken(t, "launch")(s.Launch(ctx, storeOp("l-"+tag), key, plan.ID, ""))
	lv, err := s.LaunchByID(ctx, rc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return lv
}

// Each launch Dispatch commits is read by the engine: an author with a
// space, a bot's login with brackets, a repository's name with any
// character the store keeps, a tracker reached over http on a loopback
// host, an evidence link whose scheme is upper-case, each with the steps
// handed over. ticket.md writes the words through the writer's cleaning,
// so a login reads the same in the Source part and in each block's
// opening line.
func TestDispatchPackagesAreRead(t *testing.T) {
	ctx := context.Background()
	s := dispatchStore(t)
	for i, c := range []struct{ name, page, space, author, cleanRef, cleanAuthor string }{
		{"an author with a space", "", "acme/widgets", "octo cat", "acme/widgets#1", "octo_cat"},
		{"a bot's login", "", "acme/widgets", "dependabot[bot]", "acme/widgets#2", "dependabot_bot_"},
		{"a repository of any characters", "", "acme/wid get[s]\"é", "octo-cat", "acme/wid_get_s____#3", "octo-cat"},
		{"a tracker over http on a loopback host", "http://127.0.0.1:8080/acme/widgets/issues/4", "acme/widgets", "octo-cat", "acme/widgets#4", "octo-cat"},
	} {
		n := i + 1
		page := cmp.Or(c.page, fmt.Sprintf("https://github.com/acme/widgets/issues/%d", n))
		key := taken(t, c.name)(s.ImportTicket(ctx, storeOp(fmt.Sprintf("import-%d", n)), dispatch.ImportArgs{Project: "CND", URL: page},
			itemReadAt(page, n, c.space, c.author, "SCRATCH-TITLE-FROM-OUTSIDE", "SCRATCH-LINE-FROM-OUTSIDE"))).Key
		link := taken(t, "link")(s.AddLink(ctx, storeOp(fmt.Sprintf("link-%d", n)), key, dispatch.NewLink{Kind: dispatch.LinkExternal, URL: "HTTPS://example.invalid/log"}, nil)).ID
		rp, err := readPackage(launchOf(t, s, key, []string{link}, nil))
		if err != nil {
			t.Fatalf("%s: the engine refuses the package Dispatch built: %v", c.name, err)
		}
		src := rp.p.Source
		if src == nil || src.Repro == "" || src.URL != page || len(rp.p.Evidence) != 1 || rp.p.Evidence[0].URL != "HTTPS://example.invalid/log" {
			t.Fatalf("%s: the package read: %+v %+v", c.name, src, rp.p.Evidence)
		}
		md := ticketMarkdown(rp.p)
		qs := src.quotedSource(true)
		for _, part := range []string{"- Linked item: " + c.cleanRef + ", <", "- Opened by: @" + c.cleanAuthor + " on GitHub\n",
			quoted.Opening(qs, "The description"), "opened by @" + c.cleanAuthor + " on GitHub: "} {
			if !strings.Contains(md, part) {
				t.Errorf("%s: ticket.md has no %q", c.name, part)
			}
		}
		checkMarked(t, c.name, md, qs.Clean(), "The description", "The steps to reproduce")
	}
}

// dispatchStore is a Dispatch store linked to GitHub with a project CND,
// closed with the test.
func dispatchStore(t *testing.T) *dispatch.Store {
	t.Helper()
	s, err := dispatch.OpenOptions(filepath.Join(t.TempDir(), "dispatch"), dispatch.Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	taken(t, "project")(s.CreateProject(context.Background(), storeOp("project"), dispatch.NewProject{Key: "CND", Name: "CND"}))
	return s
}

// A title and a description a read of the linked issue wrote, adopted for
// Dispatch or the ticket unlinked after the revision's approval, with no
// edit, are still quoted in the files an agent is handed: the package
// flags both, ticket.md quotes the title as its line and the description
// as its block, and task.md names the title by its line, byte for byte the
// texts of before; the approval holds. Adopted, the item still holds them
// and the quote names it; unlinked, the quote names a source not known.
func TestAdoptedTextIsQuotedInTheFiles(t *testing.T) {
	ctx := context.Background()
	for _, unlink := range []bool{false, true} {
		s := dispatchStore(t)
		key := taken(t, "import")(s.ImportTicket(ctx, storeOp("import"), dispatch.ImportArgs{Project: "CND", URL: "https://github.com/acme/widgets/issues/7"},
			itemRead(7, "acme/widgets", "octo-cat", "SCRATCH-TITLE-FROM-OUTSIDE", "SCRATCH-LINE-FROM-OUTSIDE\nPLANTED"))).Key
		raw := launchOf(t, s, key, nil, func() {
			if unlink {
				taken(t, "unlink")(s.UnlinkExternal(ctx, storeOp("unlink"), key))
				return
			}
			for _, field := range []string{dispatch.FieldTitle, dispatch.FieldDescription} {
				taken(t, "adopt "+field)(s.SetAuthority(ctx, storeOp("adopt-"+field), dispatch.AuthorityArgs{Key: key, Field: field, Owner: dispatch.OwnerDispatch}, nil))
			}
		})
		rp, err := readPackage(raw)
		if err != nil {
			t.Fatalf("unlinked %v: %v", unlink, err)
		}
		p := rp.p
		if p.Source == nil || p.Source.Unlinked != unlink || !p.Source.TitleFromGitHub || !p.Source.DescriptionFromGitHub || p.Source.TitleFromItem == unlink ||
			p.Source.DescriptionFromItem == unlink || (p.Source.Ref == "acme/widgets#7") == unlink ||
			p.Ticket.Title != "SCRATCH-TITLE-FROM-OUTSIDE" || p.Ticket.Description != "SCRATCH-LINE-FROM-OUTSIDE\nPLANTED" {
			t.Fatalf("the package, unlinked %v: %+v %q %q", unlink, p.Source, p.Ticket.Title, p.Ticket.Description)
		}
		qs := p.Source.quotedSource(!unlink)
		if unlink != (qs == quoted.Source{}) {
			t.Fatalf("the quote's source, unlinked %v: %+v", unlink, qs)
		}
		md := ticketMarkdown(p)
		if !strings.Contains(md, "\n"+quoted.Line(qs, "The title", "SCRATCH-TITLE-FROM-OUTSIDE")+"\n") ||
			!strings.Contains(md, quoted.Block(qs, "The description", "SCRATCH-LINE-FROM-OUTSIDE\nPLANTED")) ||
			strings.Contains(md, "Unlinked from the ticket before the launch") != unlink {
			t.Fatalf("ticket.md, unlinked %v:\n%s", unlink, md)
		}
		checkMarked(t, "adopted", md, qs, "The description")
		f := TaskFile{Task: "build", Attempt: 1, MaxAttempts: 3, Member: "dev", Kind: TaskWork, Goal: "g", Instructions: "Do it.", Start: strings.Repeat("b", 40),
			Ticket:      &TaskTicket{Key: p.Ticket.Key, Rev: p.Ticket.Rev, Plan: p.Plan.ID, PlanN: p.Plan.N, Digest: rp.digest, Files: packageFilePaths(p)},
			ticketTitle: cutTitle(p.Ticket.Title), ticketQuoted: quotedTitle(p), packageQuoted: quotedPackage(p)}
		task, _, _, err := fitTaskFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(task), "\n"+quoted.Line(qs, "Its title", "SCRATCH-TITLE-FROM-OUTSIDE")+"\n") {
			t.Fatalf("task.md, unlinked %v:\n%s", unlink, task)
		}
	}
}

// quotedLines is md's lines that a quote holds: each line of a block
// with its prefix (the approval's note among them), and a quoted line's
// text after its words.
func quotedLines(md string) []string {
	var out []string
	for _, l := range strings.Split(md, "\n") {
		if l == ">" || strings.HasPrefix(l, quoted.Prefix) {
			out = append(out, l)
		} else if _, text, ok := strings.Cut(l, quoted.LineHead); ok {
			out = append(out, text)
		}
	}
	return out
}

// Across an unlink the quoted texts and their marks are the same but for
// the quote's source: imported open, a refresh that closes the item with
// its title and description unchanged, approved, launched; then unlinked
// and launched again on that approval. The second launch's quoted lines
// are the first's byte for byte, its quotes name a source not known, and
// neither its ticket.md nor its package.json states the item's state. A
// text a read of one item wrote, the ticket then linked to another, is
// quoted under a source not known, the Source part naming the item linked
// now. The engine reads each package.
func TestAnUnlinkKeepsTheQuotesAndStatesNoState(t *testing.T) {
	ctx := context.Background()
	s := dispatchStore(t)
	key := taken(t, "import")(s.ImportTicket(ctx, storeOp("import"), dispatch.ImportArgs{Project: "CND", URL: "https://github.com/acme/widgets/issues/7"},
		itemRead(7, "acme/widgets", "octo-cat", "SCRATCH-TITLE-FROM-OUTSIDE", "SCRATCH-LINE-FROM-OUTSIDE\nPLANTED"))).Key
	closed := itemRead(7, "acme/widgets", "octo-cat", "SCRATCH-TITLE-FROM-OUTSIDE", "SCRATCH-LINE-FROM-OUTSIDE\nPLANTED")
	closed.Snapshot.State, closed.Snapshot.StateReason, closed.Snapshot.UpdatedAt = "closed", "completed", closed.Snapshot.UpdatedAt.Add(time.Hour)
	taken(t, "refresh")(s.RefreshExternal(ctx, storeOp("refresh"), key, closed))
	body := dispatch.PlanBody{Base: strings.Repeat("ab", 20), Crew: storeCrew, Agents: []dispatch.PlanAgent{storeAgent("claude"), storeAgent("codex")}}
	plan := taken(t, "propose")(s.ProposePlan(ctx, storeOp("propose"), key, dispatch.PlanRequest{Crew: "crew"}, body)).ID
	taken(t, "approve")(s.ApprovePlan(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: "approve", By: "Operator"}, plan, 0, "go"))
	launch := func(op string) (*runPackage, string) {
		t.Helper()
		rc := taken(t, op)(s.Launch(ctx, storeOp(op), key, plan, ""))
		lv, err := s.LaunchByID(ctx, rc.ID)
		if err != nil {
			t.Fatal(err)
		}
		rp, err := readPackage(lv.Package)
		if err != nil {
			t.Fatalf("%s: the engine refuses the package: %v", op, err)
		}
		return rp, string(lv.Package)
	}
	linked, _ := launch("linked")
	taken(t, "unlink")(s.UnlinkExternal(ctx, storeOp("unlink"), key))
	unlinked, raw := launch("unlinked")
	before, after := ticketMarkdown(linked.p), ticketMarkdown(unlinked.p)
	if !strings.Contains(before, "- State at the launch: closed (completed)") || !linked.p.Source.TitleFromItem {
		t.Fatalf("the launch before the unlink:\n%s", before)
	}
	if got, want := quotedLines(after), quotedLines(before); strings.Join(got, "\n") != strings.Join(want, "\n") || len(got) != 4 {
		t.Fatalf("the quoted lines after the unlink:\n%q\nbefore:\n%q", got, want)
	}
	unknown := quoted.Source{}
	checkMarked(t, "unlinked", after, unknown, "The description")
	if !strings.Contains(after, quoted.Block(unknown, "The description", "SCRATCH-LINE-FROM-OUTSIDE\nPLANTED")) ||
		!strings.Contains(after, "\n"+quoted.Line(unknown, "The title", "SCRATCH-TITLE-FROM-OUTSIDE")+"\n") ||
		strings.Contains(after, "State") || strings.Contains(after, "closed") || strings.Contains(after, "acme/widgets") || strings.Contains(after, "octo-cat") {
		t.Fatalf("ticket.md after the unlink:\n%s", after)
	}
	for _, word := range []string{`"state"`, `"stateReason"`, `"closed"`, `"completed"`, `"snapshotSha256"`, `"readAt"`, `"author"`, `"ref"`} {
		if strings.Contains(raw, word) {
			t.Errorf("package.json after the unlink holds %s: %s", word, raw)
		}
	}

	// Linked to another item: its read wrote neither text.
	taken(t, "link")(s.LinkExternal(ctx, storeOp("link"), dispatch.LinkArgs{Key: key, URL: "https://github.com/acme/widgets/issues/8"},
		itemRead(8, "acme/widgets", "someone-else", "Another title", "Another body")))
	plan = taken(t, "propose again")(s.ProposePlan(ctx, storeOp("propose-again"), key, dispatch.PlanRequest{Crew: "crew"}, body)).ID
	taken(t, "approve again")(s.ApprovePlan(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: "approve-again", By: "Operator"}, plan, 0, "go"))
	other, _ := launch("other")
	md := ticketMarkdown(other.p)
	if src := other.p.Source; src == nil || src.Unlinked || src.Ref != "acme/widgets#8" || !src.TitleFromGitHub || !src.DescriptionFromGitHub || src.TitleFromItem || src.DescriptionFromItem {
		t.Fatalf("the package linked to another item: %+v", other.p.Source)
	}
	checkMarked(t, "another item", md, unknown, "The description")
	if !strings.Contains(md, "- Linked item: acme/widgets#8, <") || strings.Contains(md, "quoted from acme/widgets#8") || strings.Contains(md, "@someone-else on GitHub (") {
		t.Fatalf("ticket.md linked to another item:\n%s", md)
	}
}

// The rule across an unlink, as the store and the engine hold it: a text
// a read wrote keeps its quote and its mark across an unlink; a text that
// only the linked item's snapshot matched is quoted while the ticket is
// linked and not after. A ticket made in Dispatch with an item's own
// title and description, then linked to that item (a link writes neither
// field), approved and launched: both quoted under the item's words, the
// run named by the key alone. Unlinked and launched again on that
// approval: no source, the description Dispatch's own text, the run named
// by the key and the title, as before the link. This is the rule, not a
// gap: no read wrote these texts into the ticket.
func TestASnapshotOnlyQuoteEndsWithTheLink(t *testing.T) {
	ctx := context.Background()
	const title, line = "SCRATCH-TITLE-FROM-OUTSIDE", "SCRATCH-LINE-FROM-OUTSIDE\nPLANTED"
	s := dispatchStore(t)
	key := taken(t, "create")(s.CreateTicket(ctx, storeOp("create"), dispatch.NewTicket{Project: "CND", Title: title, Description: line})).Key
	taken(t, "link")(s.LinkExternal(ctx, storeOp("link"), dispatch.LinkArgs{Key: key, URL: "https://github.com/acme/widgets/issues/7"},
		itemRead(7, "acme/widgets", "octo-cat", title, line)))
	body := dispatch.PlanBody{Base: strings.Repeat("ab", 20), Crew: storeCrew, Agents: []dispatch.PlanAgent{storeAgent("claude"), storeAgent("codex")}}
	plan := taken(t, "propose")(s.ProposePlan(ctx, storeOp("propose"), key, dispatch.PlanRequest{Crew: "crew"}, body)).ID
	taken(t, "approve")(s.ApprovePlan(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: "approve", By: "Operator"}, plan, 0, "go"))
	launch := func(op string) (*runPackage, string, string) {
		t.Helper()
		rc := taken(t, op)(s.Launch(ctx, storeOp(op), key, plan, ""))
		lv, err := s.LaunchByID(ctx, rc.ID)
		if err != nil {
			t.Fatal(err)
		}
		rp, err := readPackage(lv.Package)
		if err != nil {
			t.Fatalf("%s: the engine refuses the package: %v", op, err)
		}
		return rp, ticketMarkdown(rp.p), lv.Label
	}

	linked, md, label := launch("linked")
	src := linked.p.Source
	if src == nil || src.Unlinked || !src.TitleFromGitHub || !src.DescriptionFromGitHub || !src.TitleFromItem || !src.DescriptionFromItem {
		t.Fatalf("linked: the package's source: %+v", src)
	}
	qs := src.quotedSource(true)
	if qs != (quoted.Source{On: "GitHub", Ref: "acme/widgets#7", Author: "octo-cat"}) || label != key ||
		!strings.Contains(md, "\n"+quoted.Line(qs, "The title", title)+"\n") || !strings.Contains(md, quoted.Block(qs, "The description", line)) {
		t.Fatalf("linked: the label %q, the quote's source %+v, ticket.md:\n%s", label, qs, md)
	}
	checkMarked(t, "linked", md, qs, "The description")

	taken(t, "unlink")(s.UnlinkExternal(ctx, storeOp("unlink"), key))
	unlinked, md, label := launch("unlinked")
	if unlinked.p.Source != nil || label != key+" "+title || unlinked.p.Ticket.Title != title || unlinked.p.Ticket.Description != line ||
		!strings.Contains(md, "## Description\n\n"+line+"\n") || strings.Contains(md, " quoted from ") || strings.Contains(md, "## Source") {
		t.Fatalf("unlinked: the source %+v, the label %q, ticket.md:\n%s", unlinked.p.Source, label, md)
	}
}

// sourcePart is ticket.md's Source part, from its heading to the next.
func sourcePart(md string) string {
	_, rest, _ := strings.Cut(md, "## Source\n\n")
	part, _, _ := strings.Cut(rest, "\n## ")
	return part
}

// Whether a description is quoted is decided by its text alone, with the
// store's clock at its seam. Adopted, edited by a person and then given
// back to the linked item, the give-back stamped before the edit, or at
// the edit's stamp with the operation ids in either order: quoted. Edited
// by a person and edited back to the read's bytes, before the approval or
// between the approval and the launch: quoted, the package's source, the
// quoted lines and the Source part byte for byte the first launch's.
// Edited to a new text: Dispatch's. With the snapshot made not to hold the
// texts (adopted and the item refreshed with other bytes, or unlinked),
// both fields edited away and back to the read's bytes, the clock forward
// or stepped back, and the give-back clocks above: quoted by the reads'
// rows alone.
func TestTheQuoteIsDecidedByTheText(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	const title, line = "SCRATCH-TITLE-FROM-OUTSIDE", "SCRATCH-LINE-FROM-OUTSIDE"
	start := func(t *testing.T) (*dispatch.Store, string, func(time.Duration)) {
		s := dispatchStore(t)
		at := func(d time.Duration) { s.SetClockForTest(func() time.Time { return t0.Add(d) }) }
		at(0)
		key := taken(t, "import")(s.ImportTicket(ctx, storeOp("import"), dispatch.ImportArgs{Project: "CND", URL: "https://github.com/acme/widgets/issues/7"},
			itemRead(7, "acme/widgets", "octo-cat", title, line))).Key
		return s, key, at
	}
	adopt := func(t *testing.T, s *dispatch.Store, key string) {
		taken(t, "adopt")(s.SetAuthority(ctx, storeOp("adopt"), dispatch.AuthorityArgs{Key: key, Field: dispatch.FieldDescription, Owner: dispatch.OwnerDispatch}, nil))
	}
	edit := func(t *testing.T, s *dispatch.Store, op, key, text string) {
		taken(t, op)(s.EditTicket(ctx, storeOp(op), key, dispatch.TicketEdit{Set: dispatch.TicketSet{Description: &text}}))
	}
	read := func(t *testing.T, name string, raw []byte) (*runPackage, string) {
		t.Helper()
		rp, err := readPackage(raw)
		if err != nil {
			t.Fatalf("%s: the engine refuses the package: %v", name, err)
		}
		return rp, ticketMarkdown(rp.p)
	}
	quotedAsRead := func(t *testing.T, name string, rp *runPackage, md string) {
		t.Helper()
		src := rp.p.Source
		if src == nil || !src.DescriptionFromGitHub || !src.DescriptionFromItem || rp.p.Ticket.Description != line {
			t.Fatalf("%s: the package: %+v %q", name, src, rp.p.Ticket.Description)
		}
		checkMarked(t, name, md, src.quotedSource(true), "The description")
		if !strings.Contains(md, quoted.Block(src.quotedSource(true), "The description", line)) {
			t.Fatalf("%s: ticket.md:\n%s", name, md)
		}
	}

	clocks := []struct {
		name           string
		edit, give     time.Duration
		editOp, giveOp string
	}{
		{"given back stamped before the edit", 5 * time.Minute, 2 * time.Minute, "edit", "give"},
		{"given back at the edit's stamp, its id first", 5 * time.Minute, 5 * time.Minute, "z-edit", "a-give"},
		{"given back at the edit's stamp, the edit's id first", 5 * time.Minute, 5 * time.Minute, "a-edit", "z-give"},
	}
	for _, c := range clocks {
		t.Run(c.name, func(t *testing.T) {
			s, key, at := start(t)
			at(time.Minute)
			adopt(t, s, key)
			at(c.edit)
			edit(t, s, c.editOp, key, "Ours now.")
			at(c.give)
			r := itemRead(7, "acme/widgets", "octo-cat", title, line)
			taken(t, "give back")(s.SetAuthority(ctx, storeOp(c.giveOp), dispatch.AuthorityArgs{Key: key, Field: dispatch.FieldDescription, Owner: "github"}, &r))
			at(10 * time.Minute)
			rp, md := read(t, c.name, launchOf(t, s, key, nil, nil))
			quotedAsRead(t, c.name, rp, md)
		})
	}

	// The history clause alone: the snapshot made not to hold the texts
	// (both fields adopted and the item refreshed with other bytes, or the
	// ticket unlinked), so only the rows of the reads that wrote them quote
	// them. Quoted, the item's flags false: ticket.md and task.md quote them
	// under a source not known, the run is named by the key alone, and a
	// child's package withholds the title as its parent's.
	fields := []string{dispatch.FieldTitle, dispatch.FieldDescription}
	byHistory := func(t *testing.T, s *dispatch.Store, name, key string, unlink bool) {
		t.Helper()
		child := taken(t, "child")(s.CreateTicket(ctx, storeOp("child"), dispatch.NewTicket{Project: "CND", Title: "Child", Parent: key})).Key
		lv := launchViewAs(t, s, "launch", key, nil, nil)
		rp, md := read(t, name, lv.Package)
		p := rp.p
		if src := p.Source; src == nil || src.Unlinked != unlink || !src.TitleFromGitHub || !src.DescriptionFromGitHub || src.TitleFromItem || src.DescriptionFromItem ||
			p.Ticket.Title != title || p.Ticket.Description != line {
			t.Fatalf("%s: the package: %+v %q %q", name, src, p.Ticket.Title, p.Ticket.Description)
		}
		unknown := quoted.Source{}
		if lv.Label != key || !strings.Contains(md, "\n"+quoted.Line(unknown, "The title", title)+"\n") || !strings.Contains(md, quoted.Block(unknown, "The description", line)) {
			t.Fatalf("%s: the label %q, ticket.md:\n%s", name, lv.Label, md)
		}
		checkMarked(t, name, md, unknown, "The description")
		f := TaskFile{Task: "build", Attempt: 1, MaxAttempts: 3, Member: "dev", Kind: TaskWork, Goal: "g", Instructions: "Do it.", Start: strings.Repeat("b", 40),
			Ticket:      &TaskTicket{Key: p.Ticket.Key, Rev: p.Ticket.Rev, Plan: p.Plan.ID, PlanN: p.Plan.N, Digest: rp.digest, Files: packageFilePaths(p)},
			ticketTitle: cutTitle(p.Ticket.Title), ticketQuoted: quotedTitle(p), packageQuoted: quotedPackage(p)}
		task, _, _, err := fitTaskFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(task), "\n"+quoted.Line(unknown, "Its title", title)+"\n") {
			t.Fatalf("%s: task.md:\n%s", name, task)
		}
		cp, err := readPackage(launchAs(t, s, "child", child, nil, nil))
		if err != nil {
			t.Fatal(err)
		}
		if cp.p.Ticket.Parent == nil || cp.p.Ticket.Parent.Title != dispatch.WithheldTitle {
			t.Fatalf("%s: the child's parent: %+v", name, cp.p.Ticket.Parent)
		}
	}
	// The snapshot made not to hold the texts, and then both fields edited
	// away by a person and edited back to the read's bytes, with the store's
	// clock forward and stepped back at its seam (the edit back stamped
	// before the edit away, the refresh and the unlink): quoted, since the
	// text alone decides, never the newest row of the field.
	for _, unlink := range []bool{false, true} {
		for _, c := range []struct {
			name       string
			away, back time.Duration
		}{
			{"the clock forward", 20 * time.Minute, 30 * time.Minute},
			{"the clock stepped back", 20 * time.Minute, 5 * time.Minute},
		} {
			name := "adopted and the item refreshed with other bytes, then edited away and back, " + c.name
			if unlink {
				name = "unlinked, then edited away and back, " + c.name
			}
			t.Run(name, func(t *testing.T) {
				s, key, at := start(t)
				at(time.Minute)
				if !unlink {
					for _, f := range fields {
						taken(t, "adopt")(s.SetAuthority(ctx, storeOp("adopt-"+f), dispatch.AuthorityArgs{Key: key, Field: f, Owner: dispatch.OwnerDispatch}, nil))
					}
				}
				at(10 * time.Minute)
				if unlink {
					taken(t, "unlink")(s.UnlinkExternal(ctx, storeOp("unlink"), key))
				} else {
					other := itemRead(7, "acme/widgets", "octo-cat", "SCRATCH-OTHER-TITLE", "SCRATCH-OTHER-LINE")
					other.Snapshot.UpdatedAt = other.Snapshot.UpdatedAt.Add(time.Hour)
					taken(t, "refresh")(s.RefreshExternal(ctx, storeOp("refresh"), key, other))
				}
				at(c.away)
				ours, oursLine := "Ours now", "Ours now."
				taken(t, "edit away")(s.EditTicket(ctx, storeOp("away"), key, dispatch.TicketEdit{Set: dispatch.TicketSet{Title: &ours, Description: &oursLine}}))
				at(c.back)
				back, backLine := title, line
				taken(t, "edit back")(s.EditTicket(ctx, storeOp("back"), key, dispatch.TicketEdit{Set: dispatch.TicketSet{Title: &back, Description: &backLine}}))
				at(40 * time.Minute)
				byHistory(t, s, name, key, unlink)
			})
		}
	}
	// The same clocks as above for the title and the description, given
	// back and then the snapshot made not to hold the texts (both adopted
	// again and the item refreshed with other bytes, or the ticket
	// unlinked).
	for _, c := range clocks {
		for _, unlink := range []bool{false, true} {
			name := c.name + ", then adopted and the item refreshed with other bytes"
			if unlink {
				name = c.name + ", then unlinked"
			}
			t.Run(name, func(t *testing.T) {
				s, key, at := start(t)
				at(time.Minute)
				for _, f := range fields {
					taken(t, "adopt")(s.SetAuthority(ctx, storeOp("adopt-"+f), dispatch.AuthorityArgs{Key: key, Field: f, Owner: dispatch.OwnerDispatch}, nil))
				}
				at(c.edit)
				ours, oursLine := "Ours now", "Ours now."
				taken(t, "edit")(s.EditTicket(ctx, storeOp(c.editOp), key, dispatch.TicketEdit{Set: dispatch.TicketSet{Title: &ours, Description: &oursLine}}))
				at(c.give)
				r := itemRead(7, "acme/widgets", "octo-cat", title, line)
				for _, f := range fields {
					taken(t, "give back")(s.SetAuthority(ctx, storeOp(c.giveOp+"-"+f), dispatch.AuthorityArgs{Key: key, Field: f, Owner: "github"}, &r))
				}
				at(10 * time.Minute)
				if unlink {
					taken(t, "unlink")(s.UnlinkExternal(ctx, storeOp("unlink"), key))
				} else {
					for _, f := range fields {
						taken(t, "adopt again")(s.SetAuthority(ctx, storeOp("again-"+f), dispatch.AuthorityArgs{Key: key, Field: f, Owner: dispatch.OwnerDispatch}, nil))
					}
					other := itemRead(7, "acme/widgets", "octo-cat", "SCRATCH-OTHER-TITLE", "SCRATCH-OTHER-LINE")
					other.Snapshot.UpdatedAt = other.Snapshot.UpdatedAt.Add(time.Hour)
					taken(t, "refresh")(s.RefreshExternal(ctx, storeOp("refresh"), key, other))
				}
				byHistory(t, s, name, key, unlink)
			})
		}
	}

	for _, between := range []bool{false, true} {
		name := "edited back before the approval"
		if between {
			name = "edited back between the approval and the launch"
		}
		t.Run(name, func(t *testing.T) {
			s, key, at := start(t)
			first, was := read(t, "the first launch", launchAs(t, s, "first", key, nil, nil))
			at(time.Minute)
			back := func() {
				adopt(t, s, key)
				edit(t, s, "edit", key, "Ours now.")
				edit(t, s, "edit-back", key, line)
			}
			if !between {
				back()
			}
			rp, md := read(t, name, launchAs(t, s, "again", key, nil, func() {
				if between {
					back()
				}
			}))
			quotedAsRead(t, name, rp, md)
			if *rp.p.Source != *first.p.Source || strings.Join(quotedLines(md), "\n") != strings.Join(quotedLines(was), "\n") || sourcePart(md) != sourcePart(was) {
				t.Fatalf("%s: the files are not the first launch's:\n%s\nfirst:\n%s", name, md, was)
			}
		})
	}

	t.Run("edited to a new text", func(t *testing.T) {
		s, key, at := start(t)
		at(time.Minute)
		adopt(t, s, key)
		edit(t, s, "edit", key, "Ours now.")
		rp, md := read(t, "a new text", launchOf(t, s, key, nil, nil))
		if src := rp.p.Source; src == nil || src.DescriptionFromGitHub || src.DescriptionFromItem || !strings.Contains(md, "## Description\n\nOurs now.\n") {
			t.Fatalf("a new text: %+v\n%s", rp.p.Source, md)
		}
	})
}

// The engine's bounds are Dispatch's and their values, and each is the
// bound as written: a source or evidence part of exactly its bound read,
// one byte more refused; the ledger's bound is the package's.
func TestTheEnginesBoundsAreDispatchs(t *testing.T) {
	if maxPackageSource != dispatch.MaxPackageSource || maxPackageEvidenceJSON != dispatch.MaxPackageEvidenceJSON || ledger.MaxPackage != dispatch.MaxPackage ||
		maxPackageEvidence != dispatch.MaxPackageEvidence || maxPackageURL != dispatch.MaxLinkURL || maxEvidenceNote != dispatch.MaxEvidenceNote {
		t.Fatal("the engine's bounds are not Dispatch's")
	}
	p := sourcePackage("T", "D", "R")
	p.Source, p.Evidence = nil, nil
	whole := strings.TrimSpace(string(encodePackage(t, p).Package))
	src, _ := json.Marshal(sourcePackage("T", "D", "R").Source)
	for _, c := range []struct {
		name, part string
		bound      int
	}{{"source", string(src), maxPackageSource}, {"evidence", `[]`, maxPackageEvidenceJSON}} {
		for _, extra := range []int{0, 1} {
			// The part padded with spaces before its closing brace or bracket
			// to its bound, then one byte more.
			pad := c.bound - len(c.part) + extra
			part := c.part[:len(c.part)-1] + strings.Repeat(" ", pad) + c.part[len(c.part)-1:]
			_, err := readPackage([]byte(whole[:len(whole)-1] + `,"` + c.name + `":` + part + `}`))
			if (extra == 0) != (err == nil) {
				t.Errorf("the %s of %d bytes as written: %v", c.name, len(part), err)
			}
		}
	}
}
