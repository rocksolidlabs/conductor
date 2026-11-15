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
	plan := taken(t, "propose")(s.ProposePlan(ctx, storeOp("p-"+key), key, dispatch.PlanRequest{Crew: "crew"}, body))
	taken(t, "approve")(s.ApprovePlan(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: "a-" + key, By: "Operator"}, plan.ID, 0, "go"))
	if between != nil {
		between()
	}
	rc := taken(t, "launch")(s.Launch(ctx, storeOp("l-"+key), key, plan.ID, ""))
	lv, err := s.LaunchByID(ctx, rc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return lv.Package
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
		qs := src.quotedSource()
		for _, part := range []string{"- Linked item: " + c.cleanRef + ", <", "- Written by: @" + c.cleanAuthor + " on GitHub\n",
			quoted.Opening(qs, "The description"), "written by @" + c.cleanAuthor + " on GitHub: "} {
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
// edit, are still GitHub's in the files an agent is handed: the package
// flags both, ticket.md quotes the title as its line and the description
// as its block under the item's own words, and task.md names the title by
// its line, byte for byte the texts of before; the approval holds.
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
		if p.Source == nil || p.Source.Unlinked != unlink || !p.Source.TitleFromGitHub || !p.Source.DescriptionFromGitHub || p.Source.Ref != "acme/widgets#7" ||
			p.Ticket.Title != "SCRATCH-TITLE-FROM-OUTSIDE" || p.Ticket.Description != "SCRATCH-LINE-FROM-OUTSIDE\nPLANTED" {
			t.Fatalf("the package, unlinked %v: %+v %q %q", unlink, p.Source, p.Ticket.Title, p.Ticket.Description)
		}
		qs := p.Source.quotedSource()
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
